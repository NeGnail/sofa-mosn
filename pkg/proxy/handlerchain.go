/*
 * Licensed to the Apache Software Foundation (ASF) under one or more
 * contributor license agreements.  See the NOTICE file distributed with
 * this work for additional information regarding copyright ownership.
 * The ASF licenses this file to You under the Apache License, Version 2.0
 * (the "License"); you may not use this file except in compliance with
 * the License.  You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package proxy

import (
	"context"
	"github.com/alipay/sofa-mosn/pkg/upstream/cluster"
	"strings"

	"github.com/alipay/sofa-mosn/pkg/types"
)

func init() {
	RegisterMakeHandlerChain(DefaultMakeHandlerChain)
}

type RouteHandlerChain struct {
	ctx      context.Context
	handlers []types.RouteHandler
	index    int
}

func NewRouteHandlerChain(ctx context.Context, handlers []types.RouteHandler) *RouteHandlerChain {
	return &RouteHandlerChain{
		ctx:      ctx,
		handlers: handlers,
		index:    0,
	}
}

func (hc *RouteHandlerChain) DoNextHandler() types.Route {
	handler := hc.Next()
	if handler == nil {
		return nil
	}
	if handler.IsAvailable(hc.ctx) {
		return handler.Route()
	}
	return hc.DoNextHandler()
}
func (hc *RouteHandlerChain) Next() types.RouteHandler {
	if hc.index >= len(hc.handlers) {
		return nil
	}
	h := hc.handlers[hc.index]
	hc.index++
	return h
}

type MakeHandlerChain func(types.HeaderMap, types.Routers) *RouteHandlerChain

var makeHandlerChain MakeHandlerChain

func RegisterMakeHandlerChain(f MakeHandlerChain) {
	makeHandlerChain = f
}

type simpleHandler struct {
	route types.Route
}

func (h *simpleHandler) IsAvailable(ctx context.Context) bool {
	return true
}
func (h *simpleHandler) Route() types.Route {
	return h.route
}
func DefaultMakeHandlerChain(headers types.HeaderMap, routers types.Routers) *RouteHandlerChain {
	if r := routers.Route(headers, 1); r != nil {
		return NewRouteHandlerChain(context.Background(), []types.RouteHandler{
			&simpleHandler{route: r},
		})
	}
	return nil
}

func SofaMakeHandlerChain(headers types.HeaderMap, routers types.Routers) *RouteHandlerChain {
	// 0 前置处理
	grayZone, _ := headers.Get("grayZone")
	targetZone, _ := headers.Get("targetZone")
	currentZone, _ := headers.Get("currentZone")

	antContext := &AntContext{
		Header: headers,
	}
	context := context.WithValue(context.Background(), "AntContext", antContext)

	// 1 获取到所有需要的Route
	var ingressRoute types.Route
	var serviceRoute types.Route
	var vipGrayZoneRoute types.Route
	var vipTargetGrayzoneRoute types.Route
	var vipCurrentZoneRoute types.Route

	resultRouters := routers.GetAllRoutes(headers, 1)
	for _, route := range resultRouters {
		matchName := strings.Split(route.(types.PathMatchCriterion).Matcher(), "#")[0]
		matchValue := strings.Split(route.(types.PathMatchCriterion).Matcher(), "#")[1]

		if matchName == "service" {
			if matchValue == ".*" {
				ingressRoute = route
			} else {
				serviceRoute = route
			}
			continue
		}

		if matchName == "vip" && matchValue == grayZone {
			vipGrayZoneRoute = route
			continue
		}

		if matchName == "vip" && matchValue == targetZone {
			vipTargetGrayzoneRoute = route
			continue
		}

		if matchName == "vip" && matchValue == currentZone {
			vipCurrentZoneRoute = route
			continue
		}
	}

	// 2 构建Handler链
	if ingressRoute != nil {
		ingressHandler := &IngressRouteHandler{
			ingressRoute: ingressRoute,
		}
		return NewRouteHandlerChain(context, []types.RouteHandler{ingressHandler})
	}

	grayZoneHandler := &GrayZoneRouteHandler{
		serviceRoute: serviceRoute,
	}

	vipGrayZoneHandler := &VipGrayZoneRouteHandler{
		vipRoute: vipGrayZoneRoute,
	}

	targetZoneHandler := &TargetZoneRouteHandler{
		serviceRoute: serviceRoute,
	}

	vipTargetZoneRouteHandler := &VipTargetZoneRouteHandler{
		vipRoute: vipTargetGrayzoneRoute,
	}

	currentZoneRouteHandler := &CurrentZoneRouteHandler{
		serviceRoute: serviceRoute,
	}

	idcRouteHandler := &IdcRouteHandler{
		serviceRoute: serviceRoute,
	}

	vipCurrentZoneRouteHandler := &VipCurrentZoneRouteHandler{
		vipRoute: vipCurrentZoneRoute,
	}

	return NewRouteHandlerChain(context, []types.RouteHandler{grayZoneHandler, vipGrayZoneHandler, targetZoneHandler, vipTargetZoneRouteHandler, currentZoneRouteHandler, idcRouteHandler, vipCurrentZoneRouteHandler})
}

type AntContext struct {
	Header types.HeaderMap
}

// 1) ingress handler
type IngressRouteHandler struct {
	ingressRoute types.Route
}

func (h *IngressRouteHandler) IsAvailable(context.Context) bool {
	return h.ingressRoute != nil
}

func (h *IngressRouteHandler) Route() types.Route {
	return h.ingressRoute
}

// 2) grayZone handler
type GrayZoneRouteHandler struct {
	serviceRoute types.Route
}

func (h *GrayZoneRouteHandler) IsAvailable(ctx context.Context) bool {
	if h.serviceRoute == nil {
		return false
	}

	clusterName := h.serviceRoute.RouteRule().ClusterName()
	adapter := cluster.GetClusterMngAdapterInstance()
	snapshot := adapter.GetClusterSnapshot(context.Background(), clusterName)
	defer adapter.PutClusterSnapshot(snapshot)

	grayZone, _ := ctx.Value("AntContext").(*AntContext).Header.Get("grayZone")
	h.Route().RouteRule().UpdateMetaDataMatchCriteria(map[string]string{"zone": grayZone})

	matched := snapshot.IsExistsHosts(h.Route().RouteRule().MetadataMatchCriteria(clusterName))
	if matched {
		return true
	}
	return false
}

func (h *GrayZoneRouteHandler) Route() types.Route {
	return h.serviceRoute
}

// 3) vip grayZone handler
type VipGrayZoneRouteHandler struct {
	vipRoute types.Route
}

func (h *VipGrayZoneRouteHandler) IsAvailable(ctx context.Context) bool {
	if h.vipRoute == nil {
		return false
	}
	// 第三方组件判断
	grayComponentAllow := false
	if !grayComponentAllow {
		return false
	}

	clusterName := h.vipRoute.RouteRule().ClusterName()
	adapter := cluster.GetClusterMngAdapterInstance()
	snapshot := adapter.GetClusterSnapshot(context.Background(), clusterName)
	defer adapter.PutClusterSnapshot(snapshot)

	matched := snapshot.IsExistsHosts(h.Route().RouteRule().MetadataMatchCriteria(clusterName))
	if matched {
		return true
	}

	//todo 返回标识让handler链中断，不再走后续handler。告诉客户端无可用地址。
	return false
}

func (h *VipGrayZoneRouteHandler) Route() types.Route {
	return h.vipRoute
}

// 4)targetZone handler
type TargetZoneRouteHandler struct {
	serviceRoute types.Route
}

func (h *TargetZoneRouteHandler) IsAvailable(ctx context.Context) bool {
	if h.serviceRoute == nil {
		return false
	}

	clusterName := h.serviceRoute.RouteRule().ClusterName()
	adapter := cluster.GetClusterMngAdapterInstance()
	snapshot := adapter.GetClusterSnapshot(context.Background(), clusterName)
	defer adapter.PutClusterSnapshot(snapshot)

	targetZone, _ := ctx.Value("AntContext").(*AntContext).Header.Get("targetZone")
	h.Route().RouteRule().UpdateMetaDataMatchCriteria(map[string]string{"zone": targetZone})

	matched := snapshot.IsExistsHosts(h.Route().RouteRule().MetadataMatchCriteria(clusterName))
	if matched {
		return true
	}
	return false
}

func (h *TargetZoneRouteHandler) Route() types.Route {
	return h.serviceRoute
}

// 5) vip targetZone handler
type VipTargetZoneRouteHandler struct {
	vipRoute types.Route
}

func (h *VipTargetZoneRouteHandler) IsAvailable(ctx context.Context) bool {
	if h.vipRoute == nil {
		return false
	}

	clusterName := h.vipRoute.RouteRule().ClusterName()
	adapter := cluster.GetClusterMngAdapterInstance()
	snapshot := adapter.GetClusterSnapshot(context.Background(), clusterName)
	defer adapter.PutClusterSnapshot(snapshot)

	matched := snapshot.IsExistsHosts(h.Route().RouteRule().MetadataMatchCriteria(clusterName))
	if matched {
		return true
	}

	//todo 返回标识让handler链中断，不再走后续handler。告诉客户端无可用地址。
	return false
}

func (h *VipTargetZoneRouteHandler) Route() types.Route {
	return h.vipRoute
}

// 6) currentZone handler
type CurrentZoneRouteHandler struct {
	serviceRoute types.Route
}

func (h *CurrentZoneRouteHandler) IsAvailable(ctx context.Context) bool {
	if h.serviceRoute == nil {
		return false
	}

	clusterName := h.serviceRoute.RouteRule().ClusterName()
	adapter := cluster.GetClusterMngAdapterInstance()
	snapshot := adapter.GetClusterSnapshot(context.Background(), clusterName)
	defer adapter.PutClusterSnapshot(snapshot)

	currentZone, _ := ctx.Value("AntContext").(*AntContext).Header.Get("currentZone")
	h.Route().RouteRule().UpdateMetaDataMatchCriteria(map[string]string{"zone": currentZone})

	matched := snapshot.IsExistsHosts(h.Route().RouteRule().MetadataMatchCriteria(clusterName))
	if matched {
		return true
	}
	return false
}

func (h *CurrentZoneRouteHandler) Route() types.Route {
	return h.serviceRoute
}

// 7) idc handler
type IdcRouteHandler struct {
	serviceRoute types.Route
}

func (h *IdcRouteHandler) IsAvailable(context.Context) bool {
	if h.serviceRoute == nil {
		return false
	}

	clusterName := h.serviceRoute.RouteRule().ClusterName()
	adapter := cluster.GetClusterMngAdapterInstance()
	snapshot := adapter.GetClusterSnapshot(context.Background(), clusterName)
	defer adapter.PutClusterSnapshot(snapshot)

	zones := []string{"RZ01", "RZ02", "RZ03", "RZ04", "RZ05"}
	for _, zone := range zones {
		h.Route().RouteRule().UpdateMetaDataMatchCriteria(map[string]string{"zone": zone})
		if snapshot.IsExistsHosts(h.Route().RouteRule().MetadataMatchCriteria(clusterName)) {
			return true
		}
	}
	return false
}

func (h *IdcRouteHandler) Route() types.Route {
	return h.serviceRoute
}

// 8) vip currentZone handler
type VipCurrentZoneRouteHandler struct {
	vipRoute types.Route
}

func (h *VipCurrentZoneRouteHandler) IsAvailable(context.Context) bool {
	if h.vipRoute == nil {
		return false
	}

	clusterName := h.vipRoute.RouteRule().ClusterName()
	adapter := cluster.GetClusterMngAdapterInstance()
	snapshot := adapter.GetClusterSnapshot(context.Background(), clusterName)
	defer adapter.PutClusterSnapshot(snapshot)

	matched := snapshot.IsExistsHosts(h.Route().RouteRule().MetadataMatchCriteria(clusterName))
	if matched {
		return true
	}
	return false
}

func (h *VipCurrentZoneRouteHandler) Route() types.Route {
	return h.vipRoute
}
