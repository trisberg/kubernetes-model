/*
Copyright 2016 The Kubernetes Authors.

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

package kubefed

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"testing"

	federationapi "k8s.io/kubernetes/federation/apis/federation"
	kubefedtesting "k8s.io/kubernetes/federation/pkg/kubefed/testing"
	"k8s.io/kubernetes/pkg/api"
	"k8s.io/kubernetes/pkg/api/errors"
	"k8s.io/kubernetes/pkg/api/testapi"
	"k8s.io/kubernetes/pkg/api/unversioned"
	"k8s.io/kubernetes/pkg/client/restclient/fake"
	"k8s.io/kubernetes/pkg/client/typed/dynamic"
	cmdtesting "k8s.io/kubernetes/pkg/kubectl/cmd/testing"
	cmdutil "k8s.io/kubernetes/pkg/kubectl/cmd/util"
)

func TestUnjoinFederation(t *testing.T) {
	cmdErrMsg := ""
	cmdutil.BehaviorOnFatal(func(str string, code int) {
		cmdErrMsg = str
	})

	fakeKubeFiles, err := kubefedtesting.FakeKubeconfigFiles()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer kubefedtesting.RemoveFakeKubeconfigFiles(fakeKubeFiles)

	testCases := []struct {
		cluster            string
		wantCluster        string
		wantSecret         string
		kubeconfigGlobal   string
		kubeconfigExplicit string
		expectedServer     string
		expectedErr        string
	}{
		// Tests that the contexts and credentials are read from the
		// global, default kubeconfig and the correct cluster resource
		// is deregisterd.
		{
			cluster:            "syndicate",
			wantCluster:        "syndicate",
			wantSecret:         "",
			kubeconfigGlobal:   fakeKubeFiles[0],
			kubeconfigExplicit: "",
			expectedServer:     "https://10.20.30.40",
			expectedErr:        "",
		},
		// Tests that the contexts and credentials are read from the
		// explicit kubeconfig file specified and the correct cluster
		// resource is deregisterd. kubeconfig contains a single
		// cluster and context.
		{
			cluster:            "ally",
			wantCluster:        "ally",
			wantSecret:         "",
			kubeconfigGlobal:   fakeKubeFiles[0],
			kubeconfigExplicit: fakeKubeFiles[1],
			expectedServer:     "http://ally256.example.com:80",
			expectedErr:        "",
		},
		// Tests that the contexts and credentials are read from the
		// explicit kubeconfig file specified and the correct cluster
		// resource is deregisterd. kubeconfig consists of multiple
		// clusters and contexts.
		{
			cluster:            "confederate",
			wantCluster:        "confederate",
			wantSecret:         "",
			kubeconfigGlobal:   fakeKubeFiles[1],
			kubeconfigExplicit: fakeKubeFiles[2],
			expectedServer:     "https://10.8.8.8",
			expectedErr:        "",
		},
		// Negative test to ensure that we get the right warning
		// when the specified cluster to deregister is not found.
		{
			cluster:            "noexist",
			wantCluster:        "affiliate",
			wantSecret:         "",
			kubeconfigGlobal:   fakeKubeFiles[0],
			kubeconfigExplicit: "",
			expectedServer:     "https://10.20.30.40",
			expectedErr:        fmt.Sprintf("WARNING: cluster %q not found in federation, so its credentials' secret couldn't be deleted", "affiliate"),
		},
		// Negative test to ensure that we get the right warning
		// when the specified cluster's credentials secret is not
		// found.
		{
			cluster:            "affiliate",
			wantCluster:        "affiliate",
			wantSecret:         "noexist",
			kubeconfigGlobal:   fakeKubeFiles[0],
			kubeconfigExplicit: "",
			expectedServer:     "https://10.20.30.40",
			expectedErr:        fmt.Sprintf("WARNING: secret %q not found in the host cluster, so it couldn't be deleted", "noexist"),
		},
	}

	for i, tc := range testCases {
		cmdErrMsg = ""
		f := testUnjoinFederationFactory(tc.cluster, tc.expectedServer, tc.wantSecret)
		buf := bytes.NewBuffer([]byte{})
		errBuf := bytes.NewBuffer([]byte{})

		hostFactory := fakeUnjoinHostFactory(tc.cluster)
		adminConfig, err := kubefedtesting.NewFakeAdminConfig(hostFactory, tc.kubeconfigGlobal)
		if err != nil {
			t.Fatalf("[%d] unexpected error: %v", i, err)
		}

		cmd := NewCmdUnjoin(f, buf, errBuf, adminConfig)

		cmd.Flags().Set("kubeconfig", tc.kubeconfigExplicit)
		cmd.Flags().Set("host", "substrate")
		cmd.Run(cmd, []string{tc.wantCluster})

		if tc.expectedErr == "" {
			// uses the name from the cluster, not the response
			// Actual data passed are tested in the fake secret and cluster
			// REST clients.
			if msg := buf.String(); msg != fmt.Sprintf("Successfully removed cluster %q from federation\n", tc.cluster) {
				t.Errorf("[%d] unexpected output: %s", i, msg)
				if cmdErrMsg != "" {
					t.Errorf("[%d] unexpected error message: %s", i, cmdErrMsg)
				}
			}
		} else {
			if errMsg := errBuf.String(); errMsg != tc.expectedErr {
				t.Errorf("[%d] expected warning: %s, got: %s, output: %s", i, tc.expectedErr, errMsg, buf.String())
			}

		}
	}
}

func testUnjoinFederationFactory(name, server, secret string) cmdutil.Factory {
	urlPrefix := "/clusters/"

	cluster := fakeCluster(name, name, server)
	if secret != "" {
		cluster.Spec.SecretRef.Name = secret
	}

	f, tf, _, _ := cmdtesting.NewAPIFactory()
	codec := testapi.Federation.Codec()
	tf.ClientConfig = kubefedtesting.DefaultClientConfig()
	ns := testapi.Federation.NegotiatedSerializer()
	tf.Client = &fake.RESTClient{
		NegotiatedSerializer: ns,
		GroupName:            "federation",
		Client: fake.CreateHTTPClient(func(req *http.Request) (*http.Response, error) {
			switch p, m := req.URL.Path, req.Method; {
			case strings.HasPrefix(p, urlPrefix):
				got := strings.TrimPrefix(p, urlPrefix)
				if got != name {
					return nil, errors.NewNotFound(federationapi.Resource("clusters"), got)
				}

				switch m {
				case http.MethodGet:
					return &http.Response{StatusCode: http.StatusOK, Header: kubefedtesting.DefaultHeader(), Body: kubefedtesting.ObjBody(codec, &cluster)}, nil
				case http.MethodDelete:
					status := unversioned.Status{
						Status: "Success",
					}
					return &http.Response{StatusCode: http.StatusOK, Header: kubefedtesting.DefaultHeader(), Body: kubefedtesting.ObjBody(codec, &status)}, nil
				default:
					return nil, fmt.Errorf("unexpected method: %#v\n%#v", req.URL, req)
				}
			default:
				return nil, fmt.Errorf("unexpected request: %#v\n%#v", req.URL, req)
			}
		}),
	}
	tf.Namespace = "test"
	return f
}

func fakeUnjoinHostFactory(name string) cmdutil.Factory {
	urlPrefix := "/api/v1/namespaces/federation-system/secrets/"
	f, tf, codec, _ := cmdtesting.NewAPIFactory()
	ns := dynamic.ContentConfig().NegotiatedSerializer
	tf.ClientConfig = kubefedtesting.DefaultClientConfig()
	tf.Client = &fake.RESTClient{
		NegotiatedSerializer: ns,
		Client: fake.CreateHTTPClient(func(req *http.Request) (*http.Response, error) {
			switch p, m := req.URL.Path, req.Method; {
			case strings.HasPrefix(p, urlPrefix) && m == http.MethodDelete:
				got := strings.TrimPrefix(p, urlPrefix)
				if got != name {
					return nil, errors.NewNotFound(api.Resource("secrets"), got)
				}
				status := unversioned.Status{
					Status: "Success",
				}
				return &http.Response{StatusCode: http.StatusOK, Header: kubefedtesting.DefaultHeader(), Body: kubefedtesting.ObjBody(codec, &status)}, nil
			default:
				return nil, fmt.Errorf("unexpected request: %#v\n%#v", req.URL, req)
			}
		}),
	}
	return f
}