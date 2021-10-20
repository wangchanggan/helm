/*
Copyright The Helm Authors.

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

package helm // import "k8s.io/helm/pkg/helm"

import (
	"fmt"
	"io"
	"time"

	"golang.org/x/net/context"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"

	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"k8s.io/helm/pkg/chartutil"
	"k8s.io/helm/pkg/proto/hapi/chart"
	rls "k8s.io/helm/pkg/proto/hapi/services"
)

// maxMsgSize use 20MB as the default message size limit.
// grpc library default is 4MB
const maxMsgSize = 1024 * 1024 * 20

// Client manages client side of the Helm-Tiller protocol.
type Client struct {
	opts options
}

// NewClient creates a new client.
func NewClient(opts ...Option) *Client {
	var c Client
	// set some sane defaults
	c.Option(ConnectTimeout(5))
	return c.Option(opts...)
}

// Option configures the Helm client with the provided options.
func (h *Client) Option(opts ...Option) *Client {
	for _, opt := range opts {
		opt(&h.opts)
	}
	return h
}

// ListReleases lists the current releases.
func (h *Client) ListReleases(opts ...ReleaseListOption) (*rls.ListReleasesResponse, error) {
	reqOpts := h.opts
	for _, opt := range opts {
		opt(&reqOpts)
	}
	req := &reqOpts.listReq
	ctx := NewContext()

	if reqOpts.before != nil {
		if err := reqOpts.before(ctx, req); err != nil {
			return nil, err
		}
	}
	return h.list(ctx, req)
}

// InstallRelease loads a chart from chstr, installs it, and returns the release response.
func (h *Client) InstallRelease(chstr, ns string, opts ...InstallOption) (*rls.InstallReleaseResponse, error) {
	// load the chart to install
	chart, err := chartutil.Load(chstr)
	if err != nil {
		return nil, err
	}

	return h.InstallReleaseFromChart(chart, ns, opts...)
}

// InstallReleaseWithContext loads a chart from chstr, installs it, and returns the release response while accepting a context.
func (h *Client) InstallReleaseWithContext(ctx context.Context, chstr, ns string, opts ...InstallOption) (*rls.InstallReleaseResponse, error) {
	// load the chart to install
	chart, err := chartutil.Load(chstr)
	if err != nil {
		return nil, err
	}

	return h.installReleaseFromChartWithContext(ctx, chart, ns, opts...)
}

// InstallReleaseFromChart installs a new chart and returns the release response.
func (h *Client) InstallReleaseFromChart(chart *chart.Chart, ns string, opts ...InstallOption) (*rls.InstallReleaseResponse, error) {
	return h.installReleaseFromChartWithContext(NewContext(), chart, ns, opts...)
}

// InstallReleaseFromChartWithContext installs a new chart and returns the release response while accepting a context.
func (h *Client) InstallReleaseFromChartWithContext(ctx context.Context, chart *chart.Chart, ns string, opts ...InstallOption) (*rls.InstallReleaseResponse, error) {
	return h.installReleaseFromChartWithContext(ctx, chart, ns, opts...)
}

// InstallReleaseFromChartWithContext installs a new chart and returns the release response while accepting a context.
// 这个函数十分重要，是Client发送请求的函数。
// 该函数会接收一大堆参数，这些参数就是Helm在执行install命令时传入的各种用户指定参数。
func (h *Client) installReleaseFromChartWithContext(ctx context.Context, chart *chart.Chart, ns string, opts ...InstallOption) (*rls.InstallReleaseResponse, error) {
	// apply the install options
	reqOpts := h.opts
	for _, opt := range opts {
		opt(&reqOpts)
	}
	//将所有安装的参数统一设置到request对象中，构成结构体。
	req := &reqOpts.instReq
	req.Chart = chart
	req.Namespace = ns
	req.DryRun = reqOpts.dryRun
	req.DisableHooks = reqOpts.disableHooks
	req.DisableCrdHook = reqOpts.disableCRDHook
	req.ReuseName = reqOpts.reuseName
	ctx = FromContext(ctx)

	if reqOpts.before != nil {
		if err := reqOpts.before(ctx, req); err != nil {
			return nil, err
		}
	}
	// 将requirement.yaml中不需要的Chart从安装包结构体中移除。
	err := chartutil.ProcessRequirementsEnabled(req.Chart, req.Values)
	if err != nil {
		return nil, err
	}
	// 将父Chart中的value设置给予Chart，这样函数就实现了父Chart向子Chart传递参数。
	err = chartutil.ProcessRequirementsImportValues(req.Chart)
	if err != nil {
		return nil, err
	}

	// h.install将包装好的req发送给服务端。
	return h.install(ctx, req)
}

// DeleteRelease uninstalls a named release and returns the response.
func (h *Client) DeleteRelease(rlsName string, opts ...DeleteOption) (*rls.UninstallReleaseResponse, error) {
	// apply the uninstall options
	reqOpts := h.opts
	for _, opt := range opts {
		opt(&reqOpts)
	}

	if reqOpts.dryRun {
		// In the dry run case, just see if the release exists
		r, err := h.ReleaseContent(rlsName)
		if err != nil {
			return &rls.UninstallReleaseResponse{}, err
		}
		return &rls.UninstallReleaseResponse{Release: r.Release}, nil
	}

	req := &reqOpts.uninstallReq
	req.Name = rlsName
	req.DisableHooks = reqOpts.disableHooks
	ctx := NewContext()

	if reqOpts.before != nil {
		if err := reqOpts.before(ctx, req); err != nil {
			return nil, err
		}
	}
	return h.delete(ctx, req)
}

// UpdateRelease loads a chart from chstr and updates a release to a new/different chart.
func (h *Client) UpdateRelease(rlsName string, chstr string, opts ...UpdateOption) (*rls.UpdateReleaseResponse, error) {
	// load the chart to update
	chart, err := chartutil.Load(chstr)
	if err != nil {
		return nil, err
	}

	return h.UpdateReleaseFromChart(rlsName, chart, opts...)
}

// UpdateReleaseWithContext loads a chart from chstr and updates a release to a new/different chart while accepting a context.
func (h *Client) UpdateReleaseWithContext(ctx context.Context, rlsName string, chstr string, opts ...UpdateOption) (*rls.UpdateReleaseResponse, error) {
	// load the chart to update
	chart, err := chartutil.Load(chstr)
	if err != nil {
		return nil, err
	}

	return h.updateReleaseFromChartWithContext(ctx, rlsName, chart, opts...)
}

// UpdateReleaseFromChart updates a release to a new/different chart.
func (h *Client) UpdateReleaseFromChart(rlsName string, chart *chart.Chart, opts ...UpdateOption) (*rls.UpdateReleaseResponse, error) {
	return h.updateReleaseFromChartWithContext(NewContext(), rlsName, chart, opts...)
}

// UpdateReleaseFromChartWithContext updates a release to a new/different chart while accepting a context.
func (h *Client) UpdateReleaseFromChartWithContext(ctx context.Context, rlsName string, chart *chart.Chart, opts ...UpdateOption) (*rls.UpdateReleaseResponse, error) {
	return h.updateReleaseFromChartWithContext(ctx, rlsName, chart, opts...)
}

// updateReleaseFromChartWithContext updates a release to a new/different chart and accepts a context.
func (h *Client) updateReleaseFromChartWithContext(ctx context.Context, rlsName string, chart *chart.Chart, opts ...UpdateOption) (*rls.UpdateReleaseResponse, error) {
	// apply the update options
	reqOpts := h.opts
	for _, opt := range opts {
		opt(&reqOpts)
	}
	req := &reqOpts.updateReq
	req.Chart = chart
	req.DryRun = reqOpts.dryRun
	req.Name = rlsName
	req.DisableHooks = reqOpts.disableHooks
	req.Recreate = reqOpts.recreate
	req.Force = reqOpts.force
	req.ResetValues = reqOpts.resetValues
	req.ReuseValues = reqOpts.reuseValues
	ctx = FromContext(ctx)

	if reqOpts.before != nil {
		if err := reqOpts.before(ctx, req); err != nil {
			return nil, err
		}
	}
	err := chartutil.ProcessRequirementsEnabled(req.Chart, req.Values)
	if err != nil {
		return nil, err
	}
	err = chartutil.ProcessRequirementsImportValues(req.Chart)
	if err != nil {
		return nil, err
	}

	return h.update(ctx, req)
}

// GetVersion returns the server version.
func (h *Client) GetVersion(opts ...VersionOption) (*rls.GetVersionResponse, error) {
	reqOpts := h.opts
	for _, opt := range opts {
		opt(&reqOpts)
	}
	req := &rls.GetVersionRequest{}
	ctx := NewContext()

	if reqOpts.before != nil {
		if err := reqOpts.before(ctx, req); err != nil {
			return nil, err
		}
	}
	return h.version(ctx, req)
}

// RollbackRelease rolls back a release to the previous version.
func (h *Client) RollbackRelease(rlsName string, opts ...RollbackOption) (*rls.RollbackReleaseResponse, error) {
	reqOpts := h.opts
	for _, opt := range opts {
		opt(&reqOpts)
	}
	req := &reqOpts.rollbackReq
	req.Recreate = reqOpts.recreate
	req.Force = reqOpts.force
	req.DisableHooks = reqOpts.disableHooks
	req.DryRun = reqOpts.dryRun
	req.Name = rlsName
	ctx := NewContext()

	if reqOpts.before != nil {
		if err := reqOpts.before(ctx, req); err != nil {
			return nil, err
		}
	}
	return h.rollback(ctx, req)
}

// ReleaseStatus returns the given release's status.
func (h *Client) ReleaseStatus(rlsName string, opts ...StatusOption) (*rls.GetReleaseStatusResponse, error) {
	reqOpts := h.opts
	for _, opt := range opts {
		opt(&reqOpts)
	}
	req := &reqOpts.statusReq
	req.Name = rlsName
	ctx := NewContext()

	if reqOpts.before != nil {
		if err := reqOpts.before(ctx, req); err != nil {
			return nil, err
		}
	}
	return h.status(ctx, req)
}

// ReleaseContent returns the configuration for a given release.
func (h *Client) ReleaseContent(rlsName string, opts ...ContentOption) (*rls.GetReleaseContentResponse, error) {
	reqOpts := h.opts
	for _, opt := range opts {
		opt(&reqOpts)
	}
	req := &reqOpts.contentReq
	req.Name = rlsName
	ctx := NewContext()

	if reqOpts.before != nil {
		if err := reqOpts.before(ctx, req); err != nil {
			return nil, err
		}
	}
	return h.content(ctx, req)
}

// ReleaseHistory returns a release's revision history.
func (h *Client) ReleaseHistory(rlsName string, opts ...HistoryOption) (*rls.GetHistoryResponse, error) {
	reqOpts := h.opts
	for _, opt := range opts {
		opt(&reqOpts)
	}

	req := &reqOpts.histReq
	req.Name = rlsName
	ctx := NewContext()

	if reqOpts.before != nil {
		if err := reqOpts.before(ctx, req); err != nil {
			return nil, err
		}
	}
	return h.history(ctx, req)
}

// RunReleaseTest executes a pre-defined test on a release.
func (h *Client) RunReleaseTest(rlsName string, opts ...ReleaseTestOption) (<-chan *rls.TestReleaseResponse, <-chan error) {
	reqOpts := h.opts
	for _, opt := range opts {
		opt(&reqOpts)
	}

	req := &reqOpts.testReq
	req.Name = rlsName
	ctx := NewContext()

	return h.test(ctx, req)
}

// PingTiller pings the Tiller pod and ensures that it is up and running
func (h *Client) PingTiller() error {
	ctx := NewContext()
	return h.ping(ctx)
}

// connect returns a gRPC connection to Tiller or error. The gRPC dial options
// are constructed here.
func (h *Client) connect(ctx context.Context) (conn *grpc.ClientConn, err error) {
	// 设置grpc的参数，grpc.DialOption这里主要是默认30s超时时间，设置最大消息大小，默认20MB。
	opts := []grpc.DialOption{
		grpc.WithBlock(),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			// Send keepalive every 30 seconds to prevent the connection from
			// getting closed by upstreams
			Time: time.Duration(30) * time.Second,
		}),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(maxMsgSize)),
	}
	// 根据是否启用tls选择对应的证书信息。
	switch {
	case h.opts.useTLS:
		opts = append(opts, grpc.WithTransportCredentials(credentials.NewTLS(h.opts.tlsConfig)))
	default:
		opts = append(opts, grpc.WithInsecure())
	}
	ctx, cancel := context.WithTimeout(ctx, h.opts.connectTimeout)
	defer cancel()
	// grpc.DialContext(ctx, h.opts.host, opts...)非常重要，port-forward会建立一个本地和远程之间的连接
	// h.opts.host就是本地的连接端口，也就是说，建立与这个地址的连接，发送的数据就会直接送达远端的Tiller Pod。
	if conn, err = grpc.DialContext(ctx, h.opts.host, opts...); err != nil {
		return nil, err
	}
	return conn, nil
}

// list executes tiller.ListReleases RPC.
func (h *Client) list(ctx context.Context, req *rls.ListReleasesRequest) (*rls.ListReleasesResponse, error) {
	// 首先创建连接Tiller的grpc客户端。
	c, err := h.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer c.Close()

	rlc := rls.NewReleaseServiceClient(c)
	// 请求Tiller的rlc.ListReleases接口。
	s, err := rlc.ListReleases(ctx, req)
	if err != nil {
		return nil, err
	}
	var resp *rls.ListReleasesResponse
	// 循环接受Tiller返回的Release。
	for {
		r, err := s.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if resp == nil {
			resp = r
			continue
		}
		// 由于Release列表比较多，所以将每次获取的Release拼接成数组最终返回。
		resp.Releases = append(resp.Releases, r.GetReleases()...)
	}
	return resp, nil
}

// install executes tiller.InstallRelease RPC.
// 直接将拼装好的信息调用grpc接口发送给Tiller
func (h *Client) install(ctx context.Context, req *rls.InstallReleaseRequest) (*rls.InstallReleaseResponse, error) {
	// 首先建立一个grpc连接
	c, err := h.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer c.Close()

	rlc := rls.NewReleaseServiceClient(c)
	return rlc.InstallRelease(ctx, req)
}

// delete executes tiller.UninstallRelease RPC.
func (h *Client) delete(ctx context.Context, req *rls.UninstallReleaseRequest) (*rls.UninstallReleaseResponse, error) {
	c, err := h.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer c.Close()

	rlc := rls.NewReleaseServiceClient(c)
	return rlc.UninstallRelease(ctx, req)
}

// update executes tiller.UpdateRelease RPC.
func (h *Client) update(ctx context.Context, req *rls.UpdateReleaseRequest) (*rls.UpdateReleaseResponse, error) {
	c, err := h.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer c.Close()

	rlc := rls.NewReleaseServiceClient(c)
	return rlc.UpdateRelease(ctx, req)
}

// rollback executes tiller.RollbackRelease RPC.
func (h *Client) rollback(ctx context.Context, req *rls.RollbackReleaseRequest) (*rls.RollbackReleaseResponse, error) {
	c, err := h.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer c.Close()

	rlc := rls.NewReleaseServiceClient(c)
	return rlc.RollbackRelease(ctx, req)
}

// status executes tiller.GetReleaseStatus RPC.
func (h *Client) status(ctx context.Context, req *rls.GetReleaseStatusRequest) (*rls.GetReleaseStatusResponse, error) {
	c, err := h.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer c.Close()

	rlc := rls.NewReleaseServiceClient(c)
	return rlc.GetReleaseStatus(ctx, req)
}

// content executes tiller.GetReleaseContent RPC.
func (h *Client) content(ctx context.Context, req *rls.GetReleaseContentRequest) (*rls.GetReleaseContentResponse, error) {
	c, err := h.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer c.Close()

	rlc := rls.NewReleaseServiceClient(c)
	return rlc.GetReleaseContent(ctx, req)
}

// version executes tiller.GetVersion RPC.
func (h *Client) version(ctx context.Context, req *rls.GetVersionRequest) (*rls.GetVersionResponse, error) {
	c, err := h.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer c.Close()

	rlc := rls.NewReleaseServiceClient(c)
	return rlc.GetVersion(ctx, req)
}

// history executes tiller.GetHistory RPC.
func (h *Client) history(ctx context.Context, req *rls.GetHistoryRequest) (*rls.GetHistoryResponse, error) {
	c, err := h.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer c.Close()

	rlc := rls.NewReleaseServiceClient(c)
	return rlc.GetHistory(ctx, req)
}

// test executes tiller.TestRelease RPC.
func (h *Client) test(ctx context.Context, req *rls.TestReleaseRequest) (<-chan *rls.TestReleaseResponse, <-chan error) {
	errc := make(chan error, 1)
	c, err := h.connect(ctx)
	if err != nil {
		errc <- err
		return nil, errc
	}

	ch := make(chan *rls.TestReleaseResponse, 1)
	go func() {
		defer close(errc)
		defer close(ch)
		defer c.Close()

		rlc := rls.NewReleaseServiceClient(c)
		s, err := rlc.RunReleaseTest(ctx, req)
		if err != nil {
			errc <- err
			return
		}

		for {
			msg, err := s.Recv()
			if err == io.EOF {
				return
			}
			if err != nil {
				errc <- err
				return
			}
			ch <- msg
		}
	}()

	return ch, errc
}

// ping executes tiller.Ping RPC.
func (h *Client) ping(ctx context.Context) error {
	c, err := h.connect(ctx)
	if err != nil {
		return err
	}
	defer c.Close()

	healthClient := healthpb.NewHealthClient(c)
	resp, err := healthClient.Check(ctx, &healthpb.HealthCheckRequest{Service: "Tiller"})
	if err != nil {
		return err
	}
	switch resp.GetStatus() {
	case healthpb.HealthCheckResponse_SERVING:
		return nil
	case healthpb.HealthCheckResponse_NOT_SERVING:
		return fmt.Errorf("tiller is not serving requests at this time, Please try again later")
	default:
		return fmt.Errorf("tiller healthcheck returned an unknown status")
	}
}
