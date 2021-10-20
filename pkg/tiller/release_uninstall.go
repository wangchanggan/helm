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

package tiller

import (
	"fmt"
	"strings"

	ctx "golang.org/x/net/context"

	"k8s.io/helm/pkg/hooks"
	"k8s.io/helm/pkg/proto/hapi/release"
	"k8s.io/helm/pkg/proto/hapi/services"
	relutil "k8s.io/helm/pkg/releaseutil"
	"k8s.io/helm/pkg/timeconv"
)

// UninstallRelease deletes all of the resources associated with this release, and marks the release DELETED.
func (s *ReleaseServer) UninstallRelease(c ctx.Context, req *services.UninstallReleaseRequest) (*services.UninstallReleaseResponse, error) {
	if err := validateReleaseName(req.Name); err != nil {
		s.Log("uninstallRelease: Release name is invalid: %s", req.Name)
		return nil, err
	}

	// 首先根据传递过来的参数去configmap中获取对应的Release对象，不同的是，这里获取的是一个列表，把该名称下的Release所有的历史记录都取回来
	rels, err := s.env.Releases.History(req.Name)
	if err != nil {
		s.Log("uninstall: Release not loaded: %s", req.Name)
		return nil, err
	}
	if len(rels) < 1 {
		return nil, errMissingRelease
	}

	relutil.SortByRevision(rels)
	rel := rels[len(rels)-1]

	// TODO: Are there any cases where we want to force a delete even if it's
	// already marked deleted?
	// 如果上一次没有强制删除，即么这一次就有要判断是否会对已经制除过的Release进行强制副除操作
	if rel.Info.Status.Code == release.Status_DELETED {
		if req.Purge {
			if err := s.purgeReleases(rels...); err != nil {
				s.Log("uninstall: Failed to purge the release: %s", err)
				return nil, err
			}
			return &services.UninstallReleaseResponse{Release: rel}, nil
		}
		return nil, fmt.Errorf("the release named %q is already deleted", req.Name)
	}

	s.Log("uninstall: Deleting %s", req.Name)
	rel.Info.Status.Code = release.Status_DELETING
	rel.Info.Deleted = timeconv.Now()
	rel.Info.Description = "Deletion in progress (or silently failed)"
	res := &services.UninstallReleaseResponse{Release: rel}

	// 针对删除时需要执行的Hooks. 所有删除资源时需要执行的Hooks都是在这个函数下进行操作的
	// 如果用户指定了不执行Hooks，那么就会直接进行到下一步
	if !req.DisableHooks {
		if err := s.execHook(rel.Hooks, rel.Name, rel.Namespace, hooks.PreDelete, req.Timeout); err != nil {
			return res, err
		}
	} else {
		s.Log("delete hooks disabled for %s", req.Name)
	}

	// From here on out, the release is currently considered to be in Status_DELETING
	// state.
	// 这里首先标记Release的状态为删除中。有时删除资源的进程会比较长，所以先将状态置为删除中
	if err := s.env.Releases.Update(rel); err != nil {
		s.Log("uninstall: Failed to store updated release: %s", err)
	}

	// 进行真正的删除操作，主要是将Chart指定的资源进行移除，类比于kubectl delete -f，但是最终会留下真正的Release信息
	kept, errs := s.ReleaseModule.Delete(rel, req, s.env)
	res.Info = kept

	es := make([]string, 0, len(errs))
	for _, e := range errs {
		s.Log("error: %v", e)
		es = append(es, e.Error())
	}

	if !req.DisableHooks {
		if err := s.execHook(rel.Hooks, rel.Name, rel.Namespace, hooks.PostDelete, req.Timeout); err != nil {
			es = append(es, err.Error())
		}
	}

	rel.Info.Status.Code = release.Status_DELETED
	if req.Description == "" {
		rel.Info.Description = "Deletion complete"
	} else {
		rel.Info.Description = req.Description
	}

	// 如果指定了强制删除，就会将资源和Release信息一并删除
	if req.Purge {
		s.Log("purge requested for %s", req.Name)
		err := s.purgeReleases(rels...)
		if err != nil {
			s.Log("uninstall: Failed to purge the release: %s", err)
		}
		return res, err
	}

	if err := s.env.Releases.Update(rel); err != nil {
		s.Log("uninstall: Failed to store updated release: %s", err)
	}

	if len(es) > 0 {
		return res, fmt.Errorf("deletion completed with %d error(s): %s", len(es), strings.Join(es, "; "))
	}
	return res, nil
}

func (s *ReleaseServer) purgeReleases(rels ...*release.Release) error {
	for _, rel := range rels {
		if _, err := s.env.Releases.Delete(rel.Name, rel.Version); err != nil {
			return err
		}
	}
	return nil
}
