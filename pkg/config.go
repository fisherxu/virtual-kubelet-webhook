package pkg

import (
	"crypto/tls"
	"fmt"
	"time"

	"k8s.io/api/admissionregistration/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/golang/glog"
)

// get a clientset with in-cluster config.
func GetClient() *kubernetes.Clientset {
	config, err := rest.InClusterConfig()
	if err != nil {
		glog.Fatal(err)
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		glog.Fatal(err)
	}
	return clientset
}

func ConfigTLS(namespace string) *tls.Config {
	var err error
	host := fmt.Sprintf("virtual-kubelet-webhook.%s.svc", namespace)
	CaCert, ServerCert, ServerKey, err = GenerateSelfSignedCertKey(host, nil, nil)
	if err != nil {
		glog.Fatalf("Generate self signed certKey failed: %v", err)
	}

	sCert, err := tls.X509KeyPair(ServerCert, ServerKey)
	if err != nil {
		glog.Fatal(err)
	}

	return &tls.Config{
		Certificates: []tls.Certificate{sCert},
	}
}

// register this example webhook admission controller with the kube-apiserver
// by creating externalAdmissionHookConfigurations.
func SelfRegistration(clientset *kubernetes.Clientset, namespace string) {
	time.Sleep(2 * time.Second)
	client := clientset.AdmissionregistrationV1beta1().MutatingWebhookConfigurations()
	_, err := client.Get("virtual-kubelet-webhook", metav1.GetOptions{})
	if err == nil {
		if err2 := client.Delete("virtual-kubelet-webhook", nil); err2 != nil {
			glog.Fatal(err2)
		}
	}

	failurePolicy := v1beta1.Fail

	webhookConfig := &v1beta1.MutatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "virtual-kubelet-webhook",
			Namespace: namespace,
		},
		Webhooks: []v1beta1.Webhook{
			{
				Name: "virtual-kubelet-webhook.cci.io",
				Rules: []v1beta1.RuleWithOperations{
					{
						Operations: []v1beta1.OperationType{v1beta1.Create},
						Rule: v1beta1.Rule{
							APIGroups:   []string{""},
							APIVersions: []string{"v1"},
							Resources:   []string{"pods"},
						},
					},
				},
				FailurePolicy: &failurePolicy,
				ClientConfig: v1beta1.WebhookClientConfig{
					Service: &v1beta1.ServiceReference{
						Namespace: namespace,
						Name:      "virtual-kubelet-webhook",
					},
					CABundle: CaCert,
				},
			},
		},
	}
	if _, err := client.Create(webhookConfig); err != nil {
		glog.Fatalf("Client creation failed with %s", err)
	}
	glog.Infof("CLIENT CREATED")
}
