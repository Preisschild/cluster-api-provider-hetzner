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

// Package hcloudclient defines and implements the interface for talking to Hetzner HCloud API.
package hcloudclient

import (
	"context"
	"net"

	"github.com/hetznercloud/hcloud-go/hcloud"
)

// Client collects all methods used by the controller in the hcloud cloud API.
type Client interface {
	Close()

	CreateLoadBalancer(context.Context, hcloud.LoadBalancerCreateOpts) (*hcloud.LoadBalancer, error)
	DeleteLoadBalancer(context.Context, int) error
	ListLoadBalancers(context.Context, hcloud.LoadBalancerListOpts) ([]*hcloud.LoadBalancer, error)
	AttachLoadBalancerToNetwork(context.Context, *hcloud.LoadBalancer, hcloud.LoadBalancerAttachToNetworkOpts) error
	ChangeLoadBalancerType(context.Context, *hcloud.LoadBalancer, hcloud.LoadBalancerChangeTypeOpts) error
	ChangeLoadBalancerAlgorithm(context.Context, *hcloud.LoadBalancer, hcloud.LoadBalancerChangeAlgorithmOpts) error
	UpdateLoadBalancer(context.Context, *hcloud.LoadBalancer, hcloud.LoadBalancerUpdateOpts) (*hcloud.LoadBalancer, error)
	AddTargetServerToLoadBalancer(context.Context, hcloud.LoadBalancerAddServerTargetOpts, *hcloud.LoadBalancer) error
	DeleteTargetServerOfLoadBalancer(context.Context, *hcloud.LoadBalancer, *hcloud.Server) error
	AddIPTargetToLoadBalancer(context.Context, hcloud.LoadBalancerAddIPTargetOpts, *hcloud.LoadBalancer) error
	DeleteIPTargetOfLoadBalancer(context.Context, *hcloud.LoadBalancer, net.IP) error
	AddServiceToLoadBalancer(context.Context, *hcloud.LoadBalancer, hcloud.LoadBalancerAddServiceOpts) error
	DeleteServiceFromLoadBalancer(context.Context, *hcloud.LoadBalancer, int) error
	ListImages(context.Context, hcloud.ImageListOpts) ([]*hcloud.Image, error)
	CreateServer(context.Context, hcloud.ServerCreateOpts) (*hcloud.Server, error)
	AttachServerToNetwork(context.Context, *hcloud.Server, hcloud.ServerAttachToNetworkOpts) error
	ListServers(context.Context, hcloud.ServerListOpts) ([]*hcloud.Server, error)
	GetServer(context.Context, int) (*hcloud.Server, error)
	DeleteServer(context.Context, *hcloud.Server) error
	ListServerTypes(context.Context) ([]*hcloud.ServerType, error)
	GetServerType(context.Context, string) (*hcloud.ServerType, error)
	PowerOnServer(context.Context, *hcloud.Server) error
	ShutdownServer(context.Context, *hcloud.Server) error
	RebootServer(context.Context, *hcloud.Server) error
	CreateNetwork(context.Context, hcloud.NetworkCreateOpts) (*hcloud.Network, error)
	ListNetworks(context.Context, hcloud.NetworkListOpts) ([]*hcloud.Network, error)
	DeleteNetwork(context.Context, *hcloud.Network) error
	ListSSHKeys(context.Context, hcloud.SSHKeyListOpts) ([]*hcloud.SSHKey, error)
	CreatePlacementGroup(context.Context, hcloud.PlacementGroupCreateOpts) (*hcloud.PlacementGroup, error)
	DeletePlacementGroup(context.Context, int) error
	ListPlacementGroups(context.Context, hcloud.PlacementGroupListOpts) ([]*hcloud.PlacementGroup, error)
	AddServerToPlacementGroup(context.Context, *hcloud.Server, *hcloud.PlacementGroup) error
}

// Factory is the interface for creating new Client objects.
type Factory interface {
	NewClient(hcloudToken string) Client
}

// NewClient creates new HCloud clients.
func (f *factory) NewClient(hcloudToken string) Client {
	return &realClient{client: hcloud.NewClient(hcloud.WithToken(hcloudToken))}
}

type factory struct{}

var _ = Factory(&factory{})

// NewFactory creates a new factory for HCloud clients.
func NewFactory() Factory {
	return &factory{}
}

var _ Client = &realClient{}

type realClient struct {
	client *hcloud.Client
}

// Close implements the Close method of the HCloudClient interface.
func (c *realClient) Close() {}

func (c *realClient) CreateLoadBalancer(ctx context.Context, opts hcloud.LoadBalancerCreateOpts) (*hcloud.LoadBalancer, error) {
	res, _, err := c.client.LoadBalancer.Create(ctx, opts)
	return res.LoadBalancer, err
}

func (c *realClient) DeleteLoadBalancer(ctx context.Context, id int) error {
	_, err := c.client.LoadBalancer.Delete(ctx, &hcloud.LoadBalancer{ID: id})
	return err
}

func (c *realClient) ListLoadBalancers(ctx context.Context, opts hcloud.LoadBalancerListOpts) ([]*hcloud.LoadBalancer, error) {
	return c.client.LoadBalancer.AllWithOpts(ctx, opts)
}

func (c *realClient) AttachLoadBalancerToNetwork(ctx context.Context, lb *hcloud.LoadBalancer, opts hcloud.LoadBalancerAttachToNetworkOpts) error {
	_, _, err := c.client.LoadBalancer.AttachToNetwork(ctx, lb, opts)
	return err
}

func (c *realClient) ChangeLoadBalancerType(ctx context.Context, lb *hcloud.LoadBalancer, opts hcloud.LoadBalancerChangeTypeOpts) error {
	_, _, err := c.client.LoadBalancer.ChangeType(ctx, lb, opts)
	return err
}

func (c *realClient) ChangeLoadBalancerAlgorithm(ctx context.Context, lb *hcloud.LoadBalancer, opts hcloud.LoadBalancerChangeAlgorithmOpts) error {
	_, _, err := c.client.LoadBalancer.ChangeAlgorithm(ctx, lb, opts)
	return err
}

func (c *realClient) UpdateLoadBalancer(ctx context.Context, lb *hcloud.LoadBalancer, opts hcloud.LoadBalancerUpdateOpts) (*hcloud.LoadBalancer, error) {
	res, _, err := c.client.LoadBalancer.Update(ctx, lb, opts)
	return res, err
}

func (c *realClient) AddTargetServerToLoadBalancer(ctx context.Context, opts hcloud.LoadBalancerAddServerTargetOpts, lb *hcloud.LoadBalancer) error {
	_, _, err := c.client.LoadBalancer.AddServerTarget(ctx, lb, opts)
	return err
}

func (c *realClient) AddIPTargetToLoadBalancer(ctx context.Context, opts hcloud.LoadBalancerAddIPTargetOpts, lb *hcloud.LoadBalancer) error {
	_, _, err := c.client.LoadBalancer.AddIPTarget(ctx, lb, opts)
	return err
}

func (c *realClient) DeleteTargetServerOfLoadBalancer(ctx context.Context, lb *hcloud.LoadBalancer, server *hcloud.Server) error {
	_, _, err := c.client.LoadBalancer.RemoveServerTarget(ctx, lb, server)
	return err
}

func (c *realClient) DeleteIPTargetOfLoadBalancer(ctx context.Context, lb *hcloud.LoadBalancer, ip net.IP) error {
	_, _, err := c.client.LoadBalancer.RemoveIPTarget(ctx, lb, ip)
	return err
}

func (c *realClient) AddServiceToLoadBalancer(ctx context.Context, lb *hcloud.LoadBalancer, opts hcloud.LoadBalancerAddServiceOpts) error {
	_, _, err := c.client.LoadBalancer.AddService(ctx, lb, opts)
	return err
}

func (c *realClient) DeleteServiceFromLoadBalancer(ctx context.Context, lb *hcloud.LoadBalancer, listenPort int) error {
	_, _, err := c.client.LoadBalancer.DeleteService(ctx, lb, listenPort)
	return err
}

func (c *realClient) ListImages(ctx context.Context, opts hcloud.ImageListOpts) ([]*hcloud.Image, error) {
	return c.client.Image.AllWithOpts(ctx, opts)
}

func (c *realClient) CreateServer(ctx context.Context, opts hcloud.ServerCreateOpts) (*hcloud.Server, error) {
	res, _, err := c.client.Server.Create(ctx, opts)
	return res.Server, err
}

func (c *realClient) AttachServerToNetwork(ctx context.Context, server *hcloud.Server, opts hcloud.ServerAttachToNetworkOpts) error {
	_, _, err := c.client.Server.AttachToNetwork(ctx, server, opts)
	return err
}

func (c *realClient) ListServers(ctx context.Context, opts hcloud.ServerListOpts) ([]*hcloud.Server, error) {
	return c.client.Server.AllWithOpts(ctx, opts)
}

func (c *realClient) GetServer(ctx context.Context, id int) (*hcloud.Server, error) {
	res, _, err := c.client.Server.GetByID(ctx, id)
	return res, err
}

func (c *realClient) ListServerTypes(ctx context.Context) ([]*hcloud.ServerType, error) {
	return c.client.ServerType.All(ctx)
}

func (c *realClient) GetServerType(ctx context.Context, name string) (*hcloud.ServerType, error) {
	res, _, err := c.client.ServerType.GetByName(ctx, name)
	return res, err
}

func (c *realClient) ShutdownServer(ctx context.Context, server *hcloud.Server) error {
	_, _, err := c.client.Server.Shutdown(ctx, server)
	return err
}

func (c *realClient) RebootServer(ctx context.Context, server *hcloud.Server) error {
	_, _, err := c.client.Server.Reboot(ctx, server)
	return err
}

func (c *realClient) PowerOnServer(ctx context.Context, server *hcloud.Server) error {
	_, _, err := c.client.Server.Poweron(ctx, server)
	return err
}

func (c *realClient) DeleteServer(ctx context.Context, server *hcloud.Server) error {
	_, _, err := c.client.Server.DeleteWithResult(ctx, server)
	return err
}

func (c *realClient) CreateNetwork(ctx context.Context, opts hcloud.NetworkCreateOpts) (*hcloud.Network, error) {
	res, _, err := c.client.Network.Create(ctx, opts)
	return res, err
}

func (c *realClient) ListNetworks(ctx context.Context, opts hcloud.NetworkListOpts) ([]*hcloud.Network, error) {
	return c.client.Network.AllWithOpts(ctx, opts)
}

func (c *realClient) DeleteNetwork(ctx context.Context, network *hcloud.Network) error {
	_, err := c.client.Network.Delete(ctx, network)
	return err
}

func (c *realClient) ListSSHKeys(ctx context.Context, opts hcloud.SSHKeyListOpts) ([]*hcloud.SSHKey, error) {
	res, _, err := c.client.SSHKey.List(ctx, opts)
	return res, err
}

func (c *realClient) CreatePlacementGroup(ctx context.Context, opts hcloud.PlacementGroupCreateOpts) (*hcloud.PlacementGroup, error) {
	res, _, err := c.client.PlacementGroup.Create(ctx, opts)
	return res.PlacementGroup, err
}

func (c *realClient) DeletePlacementGroup(ctx context.Context, id int) error {
	_, err := c.client.PlacementGroup.Delete(ctx, &hcloud.PlacementGroup{ID: id})
	return err
}

func (c *realClient) ListPlacementGroups(ctx context.Context, opts hcloud.PlacementGroupListOpts) ([]*hcloud.PlacementGroup, error) {
	return c.client.PlacementGroup.AllWithOpts(ctx, opts)
}

func (c *realClient) AddServerToPlacementGroup(ctx context.Context, server *hcloud.Server, pg *hcloud.PlacementGroup) error {
	_, _, err := c.client.Server.AddToPlacementGroup(ctx, server, pg)
	return err
}
