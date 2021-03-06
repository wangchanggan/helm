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

package lint

import (
	"io/ioutil"
	"os"
	"strings"
	"testing"

	"k8s.io/helm/pkg/chartutil"
	"k8s.io/helm/pkg/lint/support"
	"k8s.io/helm/pkg/proto/hapi/chart"
)

var values = []byte{}

const (
	namespace        = "testNamespace"
	strict           = false
	badChartDir      = "rules/testdata/badchartfile"
	badValuesFileDir = "rules/testdata/badvaluesfile"
	badYamlFileDir   = "rules/testdata/albatross"
	goodChartDir     = "rules/testdata/goodone"
)

func TestBadChart(t *testing.T) {
	m := All(badChartDir, values, namespace, strict).Messages
	if len(m) != 6 {
		t.Errorf("Number of errors %v", len(m))
		t.Errorf("All didn't fail with expected errors, got %#v", m)
	}
	// There should be one INFO, 2 WARNINGs and one ERROR messages, check for them
	var i, w, e, e2, e3, e4 bool
	for _, msg := range m {
		if msg.Severity == support.InfoSev {
			if strings.Contains(msg.Err.Error(), "icon is recommended") {
				i = true
			}
		}
		if msg.Severity == support.WarningSev {
			if strings.Contains(msg.Err.Error(), "directory not found") {
				w = true
			}
		}
		if msg.Severity == support.ErrorSev {
			if strings.Contains(msg.Err.Error(), "version '0.0.0.0' is not a valid SemVer") {
				e = true
			}
			if strings.Contains(msg.Err.Error(), "name is required") {
				e2 = true
			}
			if strings.Contains(msg.Err.Error(), "directory name (badchartfile) and chart name () must be the same") {
				e3 = true
			}

			if strings.Contains(msg.Err.Error(), "apiVersion is required") {
				e4 = true
			}
		}
	}
	if !e || !e2 || !e3 || !e4 || !w || !i {
		t.Errorf("Didn't find all the expected errors, got %#v", m)
	}
}

func TestInvalidYaml(t *testing.T) {
	m := All(badYamlFileDir, values, namespace, strict).Messages
	if len(m) != 1 {
		t.Fatalf("All didn't fail with expected errors, got %#v", m)
	}
	if !strings.Contains(m[0].Err.Error(), "deliberateSyntaxError") {
		t.Errorf("All didn't have the error for deliberateSyntaxError")
	}
}

func TestBadValues(t *testing.T) {
	m := All(badValuesFileDir, values, namespace, strict).Messages
	if len(m) != 1 {
		t.Fatalf("All didn't fail with expected errors, got %#v", m)
	}
	if !strings.Contains(m[0].Err.Error(), "cannot unmarshal") {
		t.Errorf("All didn't have the error for invalid key format: %s", m[0].Err)
	}
}

func TestGoodChart(t *testing.T) {
	m := All(goodChartDir, values, namespace, strict).Messages
	if len(m) != 0 {
		t.Errorf("All failed but shouldn't have: %#v", m)
	}
}

// TestHelmCreateChart tests that a `helm create` always passes a `helm lint` test.
//
// See https://github.com/helm/helm/issues/7923
func TestHelmCreateChart(t *testing.T) {
	dir, err := ioutil.TempDir("", "-helm-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	cfile := &chart.Metadata{
		Name:        "testhelmcreatepasseslint",
		Description: "See lint_test.go",
		Version:     "0.1.0",
		AppVersion:  "1.0",
		ApiVersion:  chartutil.ApiVersionV1,
	}

	createdChart, err := chartutil.Create(cfile, dir)
	if err != nil {
		t.Error(err)
		// Fatal is bad because of the defer.
		return
	}

	m := All(createdChart, values, namespace, true).Messages
	if ll := len(m); ll != 1 {
		t.Errorf("All should have had exactly 1 error. Got %d", ll)
	} else if msg := m[0].Err.Error(); !strings.Contains(msg, "icon is recommended") {
		t.Errorf("Unexpected lint error: %s", msg)
	}
}
