/*
Copyright 2022 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package server implements functions to manage the lifecycle of HCloud servers.
package server

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hetznercloud/hcloud-go/hcloud"
	corev1 "k8s.io/api/core/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	capierrors "sigs.k8s.io/cluster-api/errors"
	"sigs.k8s.io/cluster-api/util/conditions"
	"sigs.k8s.io/cluster-api/util/record"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	infrav1 "github.com/syself/cluster-api-provider-hetzner/api/v1beta1"
	"github.com/syself/cluster-api-provider-hetzner/pkg/scope"
	hcloudutil "github.com/syself/cluster-api-provider-hetzner/pkg/services/hcloud/util"
	"github.com/syself/cluster-api-provider-hetzner/pkg/utils"
)

const (
	serverOffTimeout = 10 * time.Minute
)

var (
	errWrongLabel   = fmt.Errorf("label is wrong")
	errMissingLabel = fmt.Errorf("label is missing")
)

// Service defines struct with machine scope to reconcile HCloudMachines.
type Service struct {
	scope *scope.MachineScope
}

// NewService outs a new service with machine scope.
func NewService(scope *scope.MachineScope) *Service {
	return &Service{
		scope: scope,
	}
}

// Reconcile implements reconcilement of HCloudMachines.
func (s *Service) Reconcile(ctx context.Context) (res reconcile.Result, err error) {
	// detect failure domain
	failureDomain, err := s.scope.GetFailureDomain()
	if err != nil {
		return res, fmt.Errorf("failed to get failure domain: %w", err)
	}

	// set region in status of machine
	s.scope.SetRegion(failureDomain)

	// waiting for bootstrap data to be ready
	if !s.scope.IsBootstrapDataReady() {
		conditions.MarkFalse(
			s.scope.HCloudMachine,
			infrav1.InstanceBootstrapReadyCondition,
			infrav1.InstanceBootstrapNotReadyReason,
			clusterv1.ConditionSeverityInfo,
			"bootstrap not ready yet",
		)
		return reconcile.Result{RequeueAfter: 10 * time.Second}, nil
	}

	conditions.MarkTrue(s.scope.HCloudMachine, infrav1.InstanceBootstrapReadyCondition)

	// try to find an existing server
	server, err := s.findServer(ctx)
	if err != nil {
		return res, fmt.Errorf("failed to get server: %w", err)
	}

	// if no server is found we have to create one
	if server == nil {
		server, err = s.createServer(ctx)
		if err != nil {
			return res, fmt.Errorf("failed to create server: %w", err)
		}
	}

	s.scope.SetProviderID(server.ID)

	// update HCloudMachineStatus
	c := s.scope.HCloudMachine.Status.Conditions.DeepCopy()
	s.scope.HCloudMachine.Status = statusFromHCloudServer(server)
	s.scope.SetRegion(failureDomain)
	s.scope.HCloudMachine.Status.Conditions = c

	// validate labels
	if err := validateLabels(server, s.createLabels()); err != nil {
		err := fmt.Errorf("could not validate labels of HCloud server: %w", err)
		s.scope.SetError(err.Error(), capierrors.CreateMachineError)
		return res, nil
	}

	// analyze status of server
	switch server.Status {
	case hcloud.ServerStatusOff:
		return s.handleServerStatusOff(ctx, server)
	case hcloud.ServerStatusStarting:
		// Requeue here so that server does not switch back and forth between off and starting.
		// If we don't return here, the condition InstanceReady would get marked as true in this
		// case. However, if the server is stuck and does not power on, we should not mark the
		// condition InstanceReady as true to be able to remediate the server after a timeout.
		return reconcile.Result{RequeueAfter: 10 * time.Second}, nil
	case hcloud.ServerStatusRunning: // do nothing
	default:
		// some temporary status
		s.scope.SetReady(false)
		return reconcile.Result{RequeueAfter: 2 * time.Second}, nil
	}

	// check whether server is attached to the network
	if err := s.reconcileNetworkAttachment(ctx, server); err != nil {
		return res, fmt.Errorf("failed to reconcile network attachement: %w", err)
	}

	// nothing to do any more for worker nodes
	if !s.scope.IsControlPlane() {
		s.scope.SetReady(true)
		conditions.MarkTrue(s.scope.HCloudMachine, infrav1.InstanceReadyCondition)
		return res, nil
	}

	// all control planes have to be attached to the load balancer if it exists
	if err := s.reconcileLoadBalancerAttachment(ctx, server); err != nil {
		return res, fmt.Errorf("failed to reconcile load balancer attachement: %w", err)
	}

	s.scope.SetReady(true)
	conditions.MarkTrue(s.scope.HCloudMachine, infrav1.InstanceReadyCondition)

	return res, nil
}

// Delete implements delete method of server.
func (s *Service) Delete(ctx context.Context) (res reconcile.Result, err error) {
	server, err := s.findServer(ctx)
	if err != nil {
		return res, fmt.Errorf("failed to find server: %w", err)
	}

	// if no server has been found, then nothing can be deleted
	if server == nil {
		msg := fmt.Sprintf("Unable to delete HCloud server. Could not find matching server for %s", s.scope.Name())
		s.scope.V(1).Info(msg)
		record.Warnf(s.scope.HCloudMachine, "NoInstanceFound", msg)
		return res, nil
	}

	// control planes have to be deleted as targets of server
	if s.scope.IsControlPlane() && s.scope.HetznerCluster.Spec.ControlPlaneLoadBalancer.Enabled {
		if err := s.deleteServerOfLoadBalancer(ctx, server); err != nil {
			return res, fmt.Errorf("failed to delete attached server of loadbalancer: %w", err)
		}
	}

	// first shut the server down, then delete it
	switch server.Status {
	case hcloud.ServerStatusRunning:
		return s.handleDeleteServerStatusRunning(ctx, server)
	case hcloud.ServerStatusOff:
		return s.handleDeleteServerStatusOff(ctx, server)
	}

	return reconcile.Result{RequeueAfter: 5 * time.Second}, nil
}

func (s *Service) reconcileNetworkAttachment(ctx context.Context, server *hcloud.Server) error {
	// if no network exists, then do nothing
	if s.scope.HetznerCluster.Status.Network == nil {
		return nil
	}

	// if it is already attached to network, then do nothing
	for _, id := range s.scope.HetznerCluster.Status.Network.AttachedServers {
		if id == server.ID {
			return nil
		}
	}

	// attach server to network
	if err := s.scope.HCloudClient.AttachServerToNetwork(ctx, server, hcloud.ServerAttachToNetworkOpts{
		Network: &hcloud.Network{
			ID: s.scope.HetznerCluster.Status.Network.ID,
		},
	}); err != nil {
		hcloudutil.HandleRateLimitExceeded(s.scope.HCloudMachine, err, "AttachServerToNetwork")
		// check if network status is old and server is in fact already attached
		if hcloud.IsError(err, hcloud.ErrorCodeServerAlreadyAttached) {
			return nil
		}
		return fmt.Errorf("failed to attach server to network: %w", err)
	}

	return nil
}

func (s *Service) reconcileLoadBalancerAttachment(ctx context.Context, server *hcloud.Server) error {
	if s.scope.HetznerCluster.Status.ControlPlaneLoadBalancer == nil {
		return nil
	}

	// if already attached do nothing
	for _, target := range s.scope.HetznerCluster.Status.ControlPlaneLoadBalancer.Target {
		if target.Type == infrav1.LoadBalancerTargetTypeServer && target.ServerID == server.ID {
			return nil
		}
	}

	// we differentiate between private and public net
	var hasPrivateIP bool
	if len(server.PrivateNet) > 0 {
		hasPrivateIP = true
	}

	// if load balancer has not been attached to a network, then it cannot add a server with private IP
	if hasPrivateIP && conditions.IsFalse(s.scope.HetznerCluster, infrav1.LoadBalancerAttachedToNetworkCondition) {
		return nil
	}

	// attach only if server has private IP or public IPv4, otherwise Hetzner cannot handle it
	if server.PublicNet.IPv4.IP != nil || hasPrivateIP {
		opts := hcloud.LoadBalancerAddServerTargetOpts{
			Server:       server,
			UsePrivateIP: &hasPrivateIP,
		}
		loadBalancer := &hcloud.LoadBalancer{
			ID: s.scope.HetznerCluster.Status.ControlPlaneLoadBalancer.ID,
		}

		if err := s.scope.HCloudClient.AddTargetServerToLoadBalancer(ctx, opts, loadBalancer); err != nil {
			hcloudutil.HandleRateLimitExceeded(s.scope.HCloudMachine, err, "AddTargetServerToLoadBalancer")
			if hcloud.IsError(err, hcloud.ErrorCodeTargetAlreadyDefined) {
				return nil
			}
			return fmt.Errorf("failed to add server %d as target to load balancer: %w", server.ID, err)
		}

		record.Eventf(
			s.scope.HetznerCluster,
			"AddedAsTargetToLoadBalancer",
			"Added new server with id %d to the loadbalancer %v",
			server.ID, s.scope.HetznerCluster.Status.ControlPlaneLoadBalancer.ID)
	}
	return nil
}

func (s *Service) createServer(ctx context.Context) (*hcloud.Server, error) {
	// get userData
	userData, err := s.scope.GetRawBootstrapData(ctx)
	if err != nil {
		record.Warnf(
			s.scope.HCloudMachine,
			"FailedGetBootstrapData",
			err.Error(),
		)
		return nil, fmt.Errorf("failed to get raw bootstrap data: %s", err)
	}

	image, err := s.getServerImage(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get server image: %w", err)
	}

	automount := false
	startAfterCreate := true
	opts := hcloud.ServerCreateOpts{
		Name:   s.scope.Name(),
		Labels: s.createLabels(),
		Image:  image,
		Location: &hcloud.Location{
			Name: string(s.scope.HCloudMachine.Status.Region),
		},
		ServerType: &hcloud.ServerType{
			Name: string(s.scope.HCloudMachine.Spec.Type),
		},
		Automount:        &automount,
		StartAfterCreate: &startAfterCreate,
		UserData:         string(userData),
		PublicNet: &hcloud.ServerCreatePublicNet{
			EnableIPv4: s.scope.HCloudMachine.Spec.PublicNetwork.EnableIPv4,
			EnableIPv6: s.scope.HCloudMachine.Spec.PublicNetwork.EnableIPv6,
		},
	}

	// set placement group if necessary
	if s.scope.HCloudMachine.Spec.PlacementGroupName != nil {
		var foundPlacementGroupInStatus bool
		for _, pgSts := range s.scope.HetznerCluster.Status.HCloudPlacementGroups {
			if *s.scope.HCloudMachine.Spec.PlacementGroupName == pgSts.Name {
				foundPlacementGroupInStatus = true
				opts.PlacementGroup = &hcloud.PlacementGroup{
					ID:   pgSts.ID,
					Name: pgSts.Name,
					Type: hcloud.PlacementGroupType(pgSts.Type),
				}
			}
		}
		if !foundPlacementGroupInStatus {
			conditions.MarkFalse(s.scope.HCloudMachine,
				infrav1.InstanceReadyCondition,
				infrav1.InstanceHasNonExistingPlacementGroupReason,
				clusterv1.ConditionSeverityError,
				"Placement group %q does not exist in cluster",
				*s.scope.HCloudMachine.Spec.PlacementGroupName,
			)
			return nil, fmt.Errorf("failed to find placement group of server")
		}
	}

	sshKeySpecs := s.scope.HCloudMachine.Spec.SSHKeys

	// if no ssh keys are specified on the machine, take the ones from the cluster
	if len(sshKeySpecs) == 0 {
		sshKeySpecs = s.scope.HetznerCluster.Spec.SSHKeys.HCloud
	}

	// get all ssh keys that are stored in HCloud API
	sshKeysAPI, err := s.scope.HCloudClient.ListSSHKeys(ctx, hcloud.SSHKeyListOpts{})
	if err != nil {
		hcloudutil.HandleRateLimitExceeded(s.scope.HCloudMachine, err, "ListSSHKeys")
		return nil, fmt.Errorf("failed listing ssh heys from hcloud: %w", err)
	}

	// find matching keys and store them
	opts.SSHKeys, err = filterHCloudSSHKeys(sshKeysAPI, sshKeySpecs)
	if err != nil {
		return nil, fmt.Errorf("error with ssh keys: %w", err)
	}

	// set up network if available
	if net := s.scope.HetznerCluster.Status.Network; net != nil {
		opts.Networks = []*hcloud.Network{{
			ID: net.ID,
		}}
	}

	// if no private network exists, there must be an IPv4 for the load balancer
	if !s.scope.HetznerCluster.Spec.HCloudNetwork.Enabled {
		opts.PublicNet.EnableIPv4 = true
	}

	// Create the server
	server, err := s.scope.HCloudClient.CreateServer(ctx, opts)
	if err != nil {
		hcloudutil.HandleRateLimitExceeded(s.scope.HCloudMachine, err, "CreateServer")
		record.Warnf(s.scope.HCloudMachine,
			"FailedCreateHCloudServer",
			"Failed to create HCloud server %s: %s",
			s.scope.Name(),
			err,
		)
		return nil, fmt.Errorf("failed to create HCloud server %s: %w", s.scope.HCloudMachine.Name, err)
	}

	record.Eventf(s.scope.HCloudMachine, "SuccessfulCreate", "Created new server with id %d", server.ID)
	return server, nil
}

func (s *Service) getServerImage(ctx context.Context) (*hcloud.Image, error) {
	key := fmt.Sprintf("%s%s", infrav1.NameHetznerProviderPrefix, "image-name")

	// Get server type so we can filter for images with correct architecture
	serverType, err := s.scope.HCloudClient.GetServerType(ctx, string(s.scope.HCloudMachine.Spec.Type))
	if err != nil {
		hcloudutil.HandleRateLimitExceeded(s.scope.HCloudMachine, err, "GetServerType")
		return nil, fmt.Errorf("failed to get server type in HCloud: %w", err)
	}
	if serverType == nil {
		return nil, fmt.Errorf("server type '%s' was not found in the API", s.scope.HCloudMachine.Spec.Type)
	}

	// query for an existing image by label
	// this is needed because snapshots don't have a name, only descriptions and labels
	listOpts := hcloud.ImageListOpts{
		ListOpts: hcloud.ListOpts{
			LabelSelector: fmt.Sprintf("%s==%s", key, s.scope.HCloudMachine.Spec.ImageName),
		},
		Architecture: []hcloud.Architecture{serverType.Architecture},
	}

	images, err := s.scope.HCloudClient.ListImages(ctx, listOpts)
	if err != nil {
		hcloudutil.HandleRateLimitExceeded(s.scope.HCloudMachine, err, "ListImages")
		return nil, fmt.Errorf("failed to list images by label in HCloud: %w", err)
	}

	// query for an existing image by name.
	listOpts = hcloud.ImageListOpts{
		Name:         s.scope.HCloudMachine.Spec.ImageName,
		Architecture: []hcloud.Architecture{serverType.Architecture},
	}
	imagesByName, err := s.scope.HCloudClient.ListImages(ctx, listOpts)
	if err != nil {
		hcloudutil.HandleRateLimitExceeded(s.scope.HCloudMachine, err, "ListImages")
		return nil, fmt.Errorf("failed to list images by name in HCloud: %w", err)
	}

	images = append(images, imagesByName...)

	if len(images) > 1 {
		record.Warnf(s.scope.HCloudMachine,
			"ImageNameAmbiguous",
			"%v images have name %s",
			len(images),
			s.scope.HCloudMachine.Spec.ImageName,
		)
		return nil, fmt.Errorf("image name is ambiguous. %v images have name %s",
			len(images), s.scope.HCloudMachine.Spec.ImageName)
	}
	if len(images) == 0 {
		record.Warnf(s.scope.HCloudMachine,
			"ImageNotFound",
			"No image found with name %s",
			s.scope.HCloudMachine.Spec.ImageName,
		)
		return nil, fmt.Errorf("no image found with name %s", s.scope.HCloudMachine.Spec.ImageName)
	}

	return images[0], nil
}

func (s *Service) handleServerStatusOff(ctx context.Context, server *hcloud.Server) (res reconcile.Result, err error) {
	// Check if server is in ServerStatusOff and turn it on. This is to avoid a bug of Hetzner where
	// sometimes machines are created and not turned on

	instanceReadyCondition := conditions.Get(s.scope.HCloudMachine, infrav1.InstanceReadyCondition)
	if instanceReadyCondition != nil &&
		instanceReadyCondition.Status == corev1.ConditionFalse &&
		instanceReadyCondition.Reason == infrav1.ServerOffReason {
		if time.Now().Before(instanceReadyCondition.LastTransitionTime.Time.Add(serverOffTimeout)) {
			// Not yet timed out, try again to power on
			if err := s.scope.HCloudClient.PowerOnServer(ctx, server); err != nil {
				hcloudutil.HandleRateLimitExceeded(s.scope.HCloudMachine, err, "PowerOnServer")
				if hcloud.IsError(err, hcloud.ErrorCodeLocked) {
					// if server is locked, we just retry again
					return reconcile.Result{Requeue: true}, nil
				}
				return res, fmt.Errorf("failed to power on server: %w", err)
			}
		} else {
			// Timed out. Set failure reason
			s.scope.SetError("reached timeout of waiting for machines that are switched off", capierrors.CreateMachineError)
			return res, nil
		}
	} else {
		// No condition set yet. Try to power server on.
		if err := s.scope.HCloudClient.PowerOnServer(ctx, server); err != nil {
			hcloudutil.HandleRateLimitExceeded(s.scope.HCloudMachine, err, "PowerOnServer")
			if hcloud.IsError(err, hcloud.ErrorCodeLocked) {
				// if server is locked, we just retry again
				return reconcile.Result{Requeue: true}, nil
			}
			return res, fmt.Errorf("failed to power on server: %w", err)
		}
		conditions.MarkFalse(
			s.scope.HCloudMachine,
			infrav1.InstanceReadyCondition,
			infrav1.ServerOffReason,
			clusterv1.ConditionSeverityInfo,
			"server is switched off",
		)
	}

	// Try again in 30 sec.
	return reconcile.Result{RequeueAfter: 30 * time.Second}, nil
}

func (s *Service) handleDeleteServerStatusRunning(ctx context.Context, server *hcloud.Server) (res reconcile.Result, err error) {
	// Shut down the server if one of the two conditions apply:
	// 1. The server has not yet been tried to shut down and still is marked as "ready".
	// 2. The server has been tried to shut down without an effect and the timeout is not reached yet.

	if s.scope.HasInstanceReadyCondition() || (s.scope.HasInstanceTerminatedCondition() && !s.scope.HasShutdownTimedOut()) {
		if err := s.scope.HCloudClient.ShutdownServer(ctx, server); err != nil {
			hcloudutil.HandleRateLimitExceeded(s.scope.HCloudMachine, err, "ShutdownServer")
			return res, fmt.Errorf("failed to shutdown server: %w", err)
		}

		conditions.MarkFalse(s.scope.HCloudMachine,
			infrav1.InstanceReadyCondition,
			infrav1.InstanceTerminatedReason,
			clusterv1.ConditionSeverityInfo,
			"Instance has been shut down",
		)

		return reconcile.Result{RequeueAfter: 30 * time.Second}, nil
	}

	// timeout for shutdown has been reached - delete server
	if err := s.scope.HCloudClient.DeleteServer(ctx, server); err != nil {
		hcloudutil.HandleRateLimitExceeded(s.scope.HCloudMachine, err, "DeleteServer")
		record.Warnf(s.scope.HCloudMachine, "FailedDeleteHCloudServer", "Failed to delete HCloud server %s", s.scope.Name())
		return res, fmt.Errorf("failed to delete server: %w", err)
	}

	record.Eventf(s.scope.HCloudMachine, "HCloudServerDeleted", "HCloud server %s deleted", s.scope.Name())
	return res, nil
}

func (s *Service) handleDeleteServerStatusOff(ctx context.Context, server *hcloud.Server) (res reconcile.Result, err error) {
	// server is off and can be deleted
	if err := s.scope.HCloudClient.DeleteServer(ctx, server); err != nil {
		hcloudutil.HandleRateLimitExceeded(s.scope.HCloudMachine, err, "DeleteServer")
		record.Warnf(s.scope.HCloudMachine, "FailedDeleteHCloudServer", "Failed to delete HCloud server %s", s.scope.Name())
		return res, fmt.Errorf("failed to delete server: %w", err)
	}

	record.Eventf(s.scope.HCloudMachine, "HCloudServerDeleted", "HCloud server %s deleted", s.scope.Name())
	return res, nil
}

func (s *Service) deleteServerOfLoadBalancer(ctx context.Context, server *hcloud.Server) error {
	lb := &hcloud.LoadBalancer{ID: s.scope.HetznerCluster.Status.ControlPlaneLoadBalancer.ID}

	if err := s.scope.HCloudClient.DeleteTargetServerOfLoadBalancer(ctx, lb, server); err != nil {
		hcloudutil.HandleRateLimitExceeded(s.scope.HCloudMachine, err, "DeleteTargetServerOfLoadBalancer")
		// do not return an error in case the target was not found
		if strings.Contains(err.Error(), "load_balancer_target_not_found") {
			return nil
		}
		return fmt.Errorf("failed to delete server %v as target of load balancer %v: %w", server.ID, lb.ID, err)
	}
	record.Eventf(
		s.scope.HetznerCluster,
		"DeletedTargetOfLoadBalancer",
		"Deleted new server with id %d of the loadbalancer %v",
		server.ID, lb.ID,
	)

	return nil
}

func (s *Service) findServer(ctx context.Context) (*hcloud.Server, error) {
	var server *hcloud.Server

	// try to find the server based on its id
	serverID, err := s.scope.ServerIDFromProviderID()
	if err == nil {
		server, err = s.scope.HCloudClient.GetServer(ctx, serverID)
		if err != nil {
			hcloudutil.HandleRateLimitExceeded(s.scope.HCloudMachine, err, "GetServer")
			return nil, fmt.Errorf("failed to get server %d: %w", serverID, err)
		}

		// if server has been found, return it
		if server != nil {
			return server, nil
		}
	}

	// server has not been found via id - try to find the server based on its labels
	opts := hcloud.ServerListOpts{}

	opts.LabelSelector = utils.LabelsToLabelSelector(s.createLabels())

	servers, err := s.scope.HCloudClient.ListServers(ctx, opts)
	if err != nil {
		hcloudutil.HandleRateLimitExceeded(s.scope.HCloudMachine, err, "ListServers")
		return nil, fmt.Errorf("failed to list servers: %w", err)
	}

	if len(servers) > 1 {
		err := fmt.Errorf("found %v servers with name %s", len(servers), s.scope.Name())
		record.Warnf(s.scope.HCloudMachine, "MultipleInstances", err.Error())
		return nil, err
	}

	if len(servers) == 0 {
		return nil, nil
	}

	return servers[0], nil
}

func validateLabels(server *hcloud.Server, labels map[string]string) error {
	for key, val := range labels {
		wantVal, found := server.Labels[key]
		if !found {
			return fmt.Errorf("did not find label with key %q: %w", key, errMissingLabel)
		}
		if wantVal != val {
			return fmt.Errorf("got %q, want %q: %w", val, wantVal, errWrongLabel)
		}
	}
	return nil
}

func statusFromHCloudServer(server *hcloud.Server) infrav1.HCloudMachineStatus {
	// set instance state
	instanceState := server.Status

	// populate addresses
	addresses := []clusterv1.MachineAddress{}

	if ip := server.PublicNet.IPv4.IP.String(); ip != "" {
		addresses = append(
			addresses,
			clusterv1.MachineAddress{
				Type:    clusterv1.MachineExternalIP,
				Address: ip,
			},
		)
	}

	if ip := server.PublicNet.IPv6.IP; ip.IsGlobalUnicast() {
		ip[15]++
		addresses = append(
			addresses,
			clusterv1.MachineAddress{
				Type:    clusterv1.MachineExternalIP,
				Address: ip.String(),
			},
		)
	}

	for _, net := range server.PrivateNet {
		addresses = append(
			addresses,
			clusterv1.MachineAddress{
				Type:    clusterv1.MachineInternalIP,
				Address: net.IP.String(),
			},
		)
	}

	return infrav1.HCloudMachineStatus{
		InstanceState: &instanceState,
		Addresses:     addresses,
	}
}

func (s *Service) createLabels() map[string]string {
	var machineType string
	if s.scope.IsControlPlane() {
		machineType = "control_plane"
	} else {
		machineType = "worker"
	}

	return map[string]string{
		infrav1.NameHetznerProviderOwned + s.scope.HetznerCluster.Name: string(infrav1.ResourceLifecycleOwned),
		infrav1.MachineNameTagKey:                                      s.scope.Name(),
		"machine_type":                                                 machineType,
	}
}

func filterHCloudSSHKeys(sshKeysAPI []*hcloud.SSHKey, sshKeysSpec []infrav1.SSHKey) ([]*hcloud.SSHKey, error) {
	sshKeysAPIMap := make(map[string]*hcloud.SSHKey)
	for i, sshKey := range sshKeysAPI {
		sshKeysAPIMap[sshKey.Name] = sshKeysAPI[i]
	}
	sshKeys := make([]*hcloud.SSHKey, len(sshKeysSpec))

	for i, sshKeySpec := range sshKeysSpec {
		sshKey, ok := sshKeysAPIMap[sshKeySpec.Name]
		if !ok {
			return nil, fmt.Errorf("ssh key not found. SSH key name: %s", sshKeySpec.Name)
		}
		sshKeys[i] = sshKey
	}
	return sshKeys, nil
}
