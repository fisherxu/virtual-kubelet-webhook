package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"strings"

	"github.com/golang/glog"
	"github.com/mattbaird/jsonpatch"
	"github.com/virtual-kubelet-webhook/pkg"
	"github.com/virtual-kubelet-webhook/pkg/api"

	"k8s.io/api/admission/v1beta1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/client-go/kubernetes"
)

var (
	clientset     *kubernetes.Clientset
	runtimeScheme = runtime.NewScheme()
	codecs        = serializer.NewCodecFactory(runtimeScheme)
	deserializer  = codecs.UniversalDeserializer()

	tolerationkey   = "virtual-kubelet.io/burst-to-cci"
	tolerationvalue = "cci"
)

func handler(w http.ResponseWriter, r *http.Request) {
	var body []byte
	if r.Body != nil {
		if data, err := ioutil.ReadAll(r.Body); err == nil {
			body = data
		}
	}
	if len(body) == 0 {
		glog.Errorf("no body found")
		http.Error(w, "no body found", http.StatusBadRequest)
		return
	}

	contentType := r.Header.Get("Content-Type")
	if contentType != "application/json" {
		glog.Errorf("Wrong content type. Got: %s", contentType)
		http.Error(w, "invalid Content-Type, want `application/json`", http.StatusUnsupportedMediaType)
		return
	}

	admReq := v1beta1.AdmissionReview{}
	admResp := v1beta1.AdmissionReview{}

	if _, _, err := deserializer.Decode(body, nil, &admReq); err != nil {
		glog.Errorf("Could not decode body: %v", err)
		admResp.Response = admissionError(err)
	} else {
		admResp.Response = getAdmissionDecision(&admReq)
	}

	resp, err := json.Marshal(admResp)
	if err != nil {
		glog.Errorf("error marshalling decision: %v", err)
		http.Error(w, fmt.Sprintf("could encode response: %v", err), http.StatusInternalServerError)
	}

	if _, err := w.Write(resp); err != nil {
		glog.Errorf("error writing response %v", err)
		http.Error(w, fmt.Sprintf("could write response: %v", err), http.StatusInternalServerError)
	}
}

func admissionError(err error) *v1beta1.AdmissionResponse {
	return &v1beta1.AdmissionResponse{
		Result: &metav1.Status{Message: err.Error()},
	}
}

func getAdmissionDecision(admReq *v1beta1.AdmissionReview) *v1beta1.AdmissionResponse {
	req := admReq.Request
	var pod corev1.Pod

	err := json.Unmarshal(req.Object.Raw, &pod)
	if err != nil {
		glog.Errorf("Could not unmarshal raw object: %v", err)
		return admissionError(err)
	}

	glog.Infof("AdmissionReview for Kind=%v Namespace=%v Name=%v UID=%v Operation=%v UserInfo=%v",
		req.Kind, req.Namespace, req.Name, req.UID, req.Operation, req.UserInfo)

	var patchBytes []byte
	if shouldPatch(&pod) {
		patchBytes, err = createPatch(&pod)
		if err != nil {
			glog.Errorf("AdmissionResponse: err=%v %s %s\n", err, pod.Namespace, pod.Name)
			return &v1beta1.AdmissionResponse{
				Allowed: true,
				UID:     req.UID,
			}
		}
	} else {
		glog.Infof("Skipping inject toleration for %s %s", pod.Namespace, pod.Name)
		return &v1beta1.AdmissionResponse{
			Allowed: true,
			UID:     req.UID,
		}
	}

	jsonPatchType := v1beta1.PatchTypeJSONPatch
	return &v1beta1.AdmissionResponse{
		Allowed:   true,
		Patch:     patchBytes,
		PatchType: &jsonPatchType,
		UID:       req.UID,
	}
}

func shouldPatch(pod *corev1.Pod) bool {
	if _, ok := pod.Annotations[api.BurstToCCIAnnotation]; !ok {
		return false
	}

	var volumeCount int
	for _, volume := range pod.Spec.Volumes {
		if volume.PersistentVolumeClaim != nil {
			pvc, err := clientset.CoreV1().PersistentVolumeClaims(pod.Namespace).Get(volume.PersistentVolumeClaim.ClaimName, metav1.GetOptions{})
			if err != nil {
				return false
			}
			if needSync(pvc) {
				volumeCount++
			}
		}
		if volume.ConfigMap != nil || volume.Secret != nil {
			volumeCount++
		}
	}
	if volumeCount != len(pod.Spec.Volumes) {
		return false
	}

	if pod.Spec.HostNetwork {
		return false
	}

	return true
}

func createPatch(pod *corev1.Pod) ([]byte, error) {
	var patch []jsonpatch.JsonPatchOperation

	taintsAdd, err := getToleration()
	if err != nil {
		return nil, err
	}

	patch = append(patch, addToleration(pod.Spec.Tolerations, taintsAdd, "/spec/tolerations")...)

	return json.Marshal(patch)
}

func addToleration(target, added []corev1.Toleration, basePath string) (patch []jsonpatch.JsonPatchOperation) {
	first := len(target) == 0
	var value interface{}
	for _, add := range added {
		value = add
		path := basePath
		if first {
			first = false
			value = []corev1.Toleration{add}
		} else {
			path = path + "/-"
		}
		patch = append(patch, jsonpatch.JsonPatchOperation{
			Operation: "add",
			Path:      path,
			Value:     value,
		})
	}
	return patch
}

// needSync: for now we only sync pvc with sfs storage.
func needSync(pvc *corev1.PersistentVolumeClaim) bool {
	withNFS, withNFSProvisioner := false, false
	if value, ok := pvc.Annotations[api.StorageClassTypeAnnotation]; ok {
		withNFS = strings.Contains(value, "nfs")
	}
	if value, ok := pvc.Annotations[api.StorageProvisionerAnnotation]; ok {
		withNFSProvisioner = strings.Contains(value, "nfs")
	}
	return withNFS && withNFSProvisioner
}

// getToleration creates a toleration using the provided key/value.
// toleration effect is read from the environment
// The toleration key/value may be overwritten by the environment.
func getToleration() ([]corev1.Toleration, error) {
	key := getEnv("VK_TOLERATION_KEY", tolerationkey)
	value := getEnv("VK_TOLERATION_VALUE", tolerationvalue)
	operatorEnv := getEnv("VK_TOLERATION_OP", "Equal")
	effectEnv := getEnv("VKUBELET_TAINT_EFFECT", "NoSchedule")

	var effect corev1.TaintEffect
	switch effectEnv {
	case "NoSchedule":
		effect = corev1.TaintEffectNoSchedule
	case "NoExecute":
		effect = corev1.TaintEffectNoExecute
	case "PreferNoSchedule":
		effect = corev1.TaintEffectPreferNoSchedule
	default:
		return nil, fmt.Errorf("taint effect %q is not supported", effectEnv)
	}

	var operator corev1.TolerationOperator
	switch operatorEnv {
	case "Exists":
		operator = corev1.TolerationOpExists
	case "Equal":
		operator = corev1.TolerationOpEqual
	default:
		return nil, fmt.Errorf("taint effect %q is not supported", effectEnv)
	}

	return []corev1.Toleration{
		{
			Key:      key,
			Value:    value,
			Effect:   effect,
			Operator: operator,
		},
	}, nil
}

func getEnv(key, defaultValue string) string {
	value, found := os.LookupEnv(key)
	if found {
		return value
	}
	return defaultValue
}

func main() {
	addr := flag.String("addr", ":8080", "address to serve on")
	http.HandleFunc("/", handler)
	glog.Infof("Starting HTTPS webhook server on %+v", *addr)

	namespace := getEnv("VK_NAMESPACE", "default")
	clientset = pkg.GetClient()
	server := &http.Server{
		Addr:      *addr,
		TLSConfig: pkg.ConfigTLS(namespace),
	}

	pkg.SelfRegistration(clientset, namespace)
	server.ListenAndServeTLS("", "")
}
