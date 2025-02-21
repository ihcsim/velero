/*
Copyright 2019 the Velero contributors.

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
package nodeagent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/vmware-tanzu/velero/pkg/builder"
	"github.com/vmware-tanzu/velero/pkg/nodeagent"
	testutil "github.com/vmware-tanzu/velero/pkg/test"
)

func Test_validatePodVolumesHostPath(t *testing.T) {
	tests := []struct {
		name    string
		pods    []*corev1.Pod
		dirs    []string
		wantErr bool
	}{
		{
			name: "no error when pod volumes are present",
			pods: []*corev1.Pod{
				builder.ForPod("foo", "bar").ObjectMeta(builder.WithUID("foo")).Result(),
				builder.ForPod("zoo", "raz").ObjectMeta(builder.WithUID("zoo")).Result(),
			},
			dirs:    []string{"foo", "zoo"},
			wantErr: false,
		},
		{
			name: "no error when pod volumes are present and there are mirror pods",
			pods: []*corev1.Pod{
				builder.ForPod("foo", "bar").ObjectMeta(builder.WithUID("foo")).Result(),
				builder.ForPod("zoo", "raz").ObjectMeta(builder.WithUID("zoo"), builder.WithAnnotations(corev1.MirrorPodAnnotationKey, "baz")).Result(),
			},
			dirs:    []string{"foo", "baz"},
			wantErr: false,
		},
		{
			name: "error when all pod volumes missing",
			pods: []*corev1.Pod{
				builder.ForPod("foo", "bar").ObjectMeta(builder.WithUID("foo")).Result(),
				builder.ForPod("zoo", "raz").ObjectMeta(builder.WithUID("zoo")).Result(),
			},
			dirs:    []string{"unexpected-dir"},
			wantErr: true,
		},
		{
			name: "error when some pod volumes missing",
			pods: []*corev1.Pod{
				builder.ForPod("foo", "bar").ObjectMeta(builder.WithUID("foo")).Result(),
				builder.ForPod("zoo", "raz").ObjectMeta(builder.WithUID("zoo")).Result(),
			},
			dirs:    []string{"foo"},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := testutil.NewFakeFileSystem()

			for _, dir := range tt.dirs {
				err := fs.MkdirAll(filepath.Join("/host_pods/", dir), os.ModePerm)
				if err != nil {
					t.Error(err)
				}
			}

			kubeClient := fake.NewSimpleClientset()
			for _, pod := range tt.pods {
				_, err := kubeClient.CoreV1().Pods(pod.GetNamespace()).Create(context.TODO(), pod, metav1.CreateOptions{})
				if err != nil {
					t.Error(err)
				}
			}

			s := &nodeAgentServer{
				logger:     testutil.NewLogger(),
				fileSystem: fs,
			}

			err := s.validatePodVolumesHostPath(kubeClient)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func Test_getDataPathConcurrentNum(t *testing.T) {
	defaultNum := 100001
	globalNum := 6
	nodeName := "node-agent-node"
	node1 := builder.ForNode("node-agent-node").Result()
	node2 := builder.ForNode("node-agent-node").Labels(map[string]string{
		"host-name": "node-1",
		"xxxx":      "yyyyy",
	}).Result()

	invalidLabelSelector := metav1.LabelSelector{
		MatchLabels: map[string]string{
			"inva/lid": "inva/lid",
		},
	}
	validLabelSelector1 := metav1.LabelSelector{
		MatchLabels: map[string]string{
			"host-name": "node-1",
		},
	}
	validLabelSelector2 := metav1.LabelSelector{
		MatchLabels: map[string]string{
			"xxxx": "yyyyy",
		},
	}

	tests := []struct {
		name          string
		getFunc       func(context.Context, string, kubernetes.Interface) (*nodeagent.Configs, error)
		setKubeClient bool
		kubeClientObj []runtime.Object
		expectNum     int
		expectLog     string
	}{
		{
			name: "failed to get configs",
			getFunc: func(context.Context, string, kubernetes.Interface) (*nodeagent.Configs, error) {
				return nil, errors.New("fake-get-error")
			},
			expectLog: "Failed to get node agent configs",
			expectNum: defaultNum,
		},
		{
			name: "configs cm not found",
			getFunc: func(context.Context, string, kubernetes.Interface) (*nodeagent.Configs, error) {
				return nil, nil
			},
			expectLog: fmt.Sprintf("Concurrency configs are not found, use the default number %v", defaultNum),
			expectNum: defaultNum,
		},
		{
			name: "configs cm's data path concurrency is nil",
			getFunc: func(context.Context, string, kubernetes.Interface) (*nodeagent.Configs, error) {
				return &nodeagent.Configs{}, nil
			},
			expectLog: fmt.Sprintf("Concurrency configs are not found, use the default number %v", defaultNum),
			expectNum: defaultNum,
		},
		{
			name: "global number is invalid",
			getFunc: func(context.Context, string, kubernetes.Interface) (*nodeagent.Configs, error) {
				return &nodeagent.Configs{
					DataPathConcurrency: &nodeagent.DataPathConcurrency{
						GlobalConfig: -1,
					},
				}, nil
			},
			expectLog: fmt.Sprintf("Global number %v is invalid, use the default value %v", -1, defaultNum),
			expectNum: defaultNum,
		},
		{
			name: "global number is valid",
			getFunc: func(context.Context, string, kubernetes.Interface) (*nodeagent.Configs, error) {
				return &nodeagent.Configs{
					DataPathConcurrency: &nodeagent.DataPathConcurrency{
						GlobalConfig: globalNum,
					},
				}, nil
			},
			expectNum: globalNum,
		},
		{
			name: "node is not found",
			getFunc: func(context.Context, string, kubernetes.Interface) (*nodeagent.Configs, error) {
				return &nodeagent.Configs{
					DataPathConcurrency: &nodeagent.DataPathConcurrency{
						GlobalConfig: globalNum,
						PerNodeConfig: []nodeagent.RuledConfigs{
							{
								Number: 100,
							},
						},
					},
				}, nil
			},
			setKubeClient: true,
			expectLog:     fmt.Sprintf("Failed to get node info for %s, use the global number %v", nodeName, globalNum),
			expectNum:     globalNum,
		},
		{
			name: "failed to get selector",
			getFunc: func(context.Context, string, kubernetes.Interface) (*nodeagent.Configs, error) {
				return &nodeagent.Configs{
					DataPathConcurrency: &nodeagent.DataPathConcurrency{
						GlobalConfig: globalNum,
						PerNodeConfig: []nodeagent.RuledConfigs{
							{
								NodeSelector: invalidLabelSelector,
								Number:       100,
							},
						},
					},
				}, nil
			},
			setKubeClient: true,
			kubeClientObj: []runtime.Object{node1},
			expectLog:     fmt.Sprintf("Failed to parse rule with label selector %s, skip it", invalidLabelSelector.String()),
			expectNum:     globalNum,
		},
		{
			name: "rule number is invalid",
			getFunc: func(context.Context, string, kubernetes.Interface) (*nodeagent.Configs, error) {
				return &nodeagent.Configs{
					DataPathConcurrency: &nodeagent.DataPathConcurrency{
						GlobalConfig: globalNum,
						PerNodeConfig: []nodeagent.RuledConfigs{
							{
								NodeSelector: validLabelSelector1,
								Number:       -1,
							},
						},
					},
				}, nil
			},
			setKubeClient: true,
			kubeClientObj: []runtime.Object{node1},
			expectLog:     fmt.Sprintf("Rule with label selector %s is with an invalid number %v, skip it", validLabelSelector1.String(), -1),
			expectNum:     globalNum,
		},
		{
			name: "label doesn't match",
			getFunc: func(context.Context, string, kubernetes.Interface) (*nodeagent.Configs, error) {
				return &nodeagent.Configs{
					DataPathConcurrency: &nodeagent.DataPathConcurrency{
						GlobalConfig: globalNum,
						PerNodeConfig: []nodeagent.RuledConfigs{
							{
								NodeSelector: validLabelSelector1,
								Number:       -1,
							},
						},
					},
				}, nil
			},
			setKubeClient: true,
			kubeClientObj: []runtime.Object{node1},
			expectLog:     fmt.Sprintf("Per node number for node %s is not found, use the global number %v", nodeName, globalNum),
			expectNum:     globalNum,
		},
		{
			name: "match one rule",
			getFunc: func(context.Context, string, kubernetes.Interface) (*nodeagent.Configs, error) {
				return &nodeagent.Configs{
					DataPathConcurrency: &nodeagent.DataPathConcurrency{
						GlobalConfig: globalNum,
						PerNodeConfig: []nodeagent.RuledConfigs{
							{
								NodeSelector: validLabelSelector1,
								Number:       66,
							},
						},
					},
				}, nil
			},
			setKubeClient: true,
			kubeClientObj: []runtime.Object{node2},
			expectLog:     fmt.Sprintf("Use the per node number %v over global number %v for node %s", 66, globalNum, nodeName),
			expectNum:     66,
		},
		{
			name: "match multiple rules",
			getFunc: func(context.Context, string, kubernetes.Interface) (*nodeagent.Configs, error) {
				return &nodeagent.Configs{
					DataPathConcurrency: &nodeagent.DataPathConcurrency{
						GlobalConfig: globalNum,
						PerNodeConfig: []nodeagent.RuledConfigs{
							{
								NodeSelector: validLabelSelector1,
								Number:       66,
							},
							{
								NodeSelector: validLabelSelector2,
								Number:       36,
							},
						},
					},
				}, nil
			},
			setKubeClient: true,
			kubeClientObj: []runtime.Object{node2},
			expectLog:     fmt.Sprintf("Use the per node number %v over global number %v for node %s", 36, globalNum, nodeName),
			expectNum:     36,
		},
		{
			name: "match multiple rules 2",
			getFunc: func(context.Context, string, kubernetes.Interface) (*nodeagent.Configs, error) {
				return &nodeagent.Configs{
					DataPathConcurrency: &nodeagent.DataPathConcurrency{
						GlobalConfig: globalNum,
						PerNodeConfig: []nodeagent.RuledConfigs{
							{
								NodeSelector: validLabelSelector1,
								Number:       36,
							},
							{
								NodeSelector: validLabelSelector2,
								Number:       66,
							},
						},
					},
				}, nil
			},
			setKubeClient: true,
			kubeClientObj: []runtime.Object{node2},
			expectLog:     fmt.Sprintf("Use the per node number %v over global number %v for node %s", 36, globalNum, nodeName),
			expectNum:     36,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fakeKubeClient := fake.NewSimpleClientset(test.kubeClientObj...)

			logBuffer := ""

			s := &nodeAgentServer{
				nodeName: nodeName,
				logger:   testutil.NewSingleLogger(&logBuffer),
			}

			if test.setKubeClient {
				s.kubeClient = fakeKubeClient
			}

			getConfigsFunc = test.getFunc

			num := s.getDataPathConcurrentNum(defaultNum)
			assert.Equal(t, test.expectNum, num)
			if test.expectLog == "" {
				assert.Equal(t, "", logBuffer)
			} else {
				assert.True(t, strings.Contains(logBuffer, test.expectLog))
			}
		})
	}
}
