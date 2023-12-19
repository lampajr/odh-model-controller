package e2e

import (
	"fmt"
	"os/exec"
	"time"

	kservev1beta1 "github.com/kserve/kserve/pkg/apis/serving/v1beta1"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/opendatahub-io/model-registry/pkg/api"
	"github.com/opendatahub-io/model-registry/pkg/core"
	"github.com/opendatahub-io/model-registry/pkg/openapi"
	"github.com/opendatahub-io/odh-model-controller/test/utils"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	err                 error
	modelRegistryClient api.ModelRegistryApi
)

// model registry data
var (
	registeredModel *openapi.RegisteredModel
	modelVersion    *openapi.ModelVersion
	modelArtifact   *openapi.ModelArtifact
	// data
	modelName            = "dummy-model"
	versionName          = "dummy-version"
	modelFormatName      = "onnx"
	modelFormatVersion   = "1"
	storagePath          = "path/to/model"
	storageKey           = "aws-connection-models"
	inferenceServiceName = "dummy-inference-service"
	modelServer          = "ovms-1.x" // applied ServingRuntime
)

// Run model registry and serving e2e test assuming the controller is already deployed in the cluster
var _ = Describe("ModelRegistry controller e2e", func() {

	BeforeEach(func() {
		// Deploy model registry
		By("by starting up model registry")
		modelRegistryClient = deployAndCheckModelRegistry()
	})

	AfterEach(func() {
		// Cleanup model registry
		By("by tearing down model registry")
		undeployModelRegistry()
	})

	When("ServingRuntime is created", func() {
		BeforeEach(func() {
			// fill mr with some models
			registeredModel, modelVersion, modelArtifact = fillModelRegistryContent(modelRegistryClient)

			_, err := utils.Run(exec.Command("kubectl", "apply", "-f", ServingRuntimePath1))
			Expect(err).ToNot(HaveOccurred())
		})

		It("the controller should properly create an ISVC based on InferenceService in model registry", func() {
			expectedServingEnvName := WorkingNamespace
			servingEnvId := checkServingEnvironment(expectedServingEnvName)

			By("checking an InferenceService is created in the model registry")
			is, err := modelRegistryClient.UpsertInferenceService(&openapi.InferenceService{
				Name:                 &inferenceServiceName,
				RegisteredModelId:    registeredModel.GetId(),
				ServingEnvironmentId: servingEnvId,
				Runtime:              &modelServer,
				DesiredState:         openapi.INFERENCESERVICESTATE_DEPLOYED.Ptr(),
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(is).ToNot(BeNil())

			Eventually(func() error {
				isvc := &kservev1beta1.InferenceService{}
				key := types.NamespacedName{Name: inferenceServiceName, Namespace: WorkingNamespace}
				return cli.Get(ctx, key, isvc)
			}, timeout, interval).ShouldNot(HaveOccurred())
		})

		It("the controller should properly delete an ISVC based on InferenceService in model registry", func() {
			expectedServingEnvName := WorkingNamespace
			servingEnvId := checkServingEnvironment(expectedServingEnvName)

			By("checking an InferenceService is created in the model registry")
			is, err := modelRegistryClient.UpsertInferenceService(&openapi.InferenceService{
				Name:                 &inferenceServiceName,
				RegisteredModelId:    registeredModel.GetId(),
				ServingEnvironmentId: servingEnvId,
				Runtime:              &modelServer,
				DesiredState:         openapi.INFERENCESERVICESTATE_UNDEPLOYED.Ptr(),
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(is).ToNot(BeNil())

			Eventually(func() error {
				isvc := &kservev1beta1.InferenceService{}
				key := types.NamespacedName{Name: inferenceServiceName, Namespace: WorkingNamespace}
				return cli.Get(ctx, key, isvc)
			}, timeout, interval).ShouldNot(HaveOccurred())
		})
	})
})

// UTILS

func checkServingEnvironment(expectedServingEnvName string) string {
	By("checking the controller has created the corresponding ServingEnvironmnet in model registry")
	var servingEnvId *string
	Eventually(func() error {
		se, err := modelRegistryClient.GetServingEnvironmentByParams(&expectedServingEnvName, nil)
		if err == nil {
			servingEnvId = se.Id
		}
		return err
	}, time.Second*20, interval).ShouldNot(HaveOccurred())
	Expect(servingEnvId).ToNot(BeNil())

	return *servingEnvId
}

// deployAndCheckModelRegistry setup model registry deployments and creates model registry client connection
func deployAndCheckModelRegistry() api.ModelRegistryApi {
	cmd := exec.Command("kubectl", "apply", "-f", ModelRegistryDatabaseDeploymentPath)
	_, err := utils.Run(cmd)
	Expect(err).ToNot(HaveOccurred())

	cmd = exec.Command("kubectl", "apply", "-f", ModelRegistryDeploymentPath)
	_, err = utils.Run(cmd)
	Expect(err).ToNot(HaveOccurred())

	waitForModelRegistryStartup()

	// retrieve model registry service
	opts := []client.ListOption{client.InNamespace(WorkingNamespace), client.MatchingLabels{
		"component": "model-registry",
	}}
	mrServiceList := &corev1.ServiceList{}
	err = cli.List(ctx, mrServiceList, opts...)
	Expect(err).ToNot(HaveOccurred())
	Expect(len(mrServiceList.Items)).To(Equal(1))

	var grpcPort *int32
	for _, port := range mrServiceList.Items[0].Spec.Ports {
		if port.Name == "grpc-api" {
			grpcPort = &port.NodePort
			break
		}
	}
	Expect(grpcPort).ToNot(BeNil())

	mlmdAddr := fmt.Sprintf("localhost:%d", *grpcPort)
	grpcConn, err := grpc.DialContext(
		ctx,
		mlmdAddr,
		grpc.WithReturnConnectionError(),
		grpc.WithBlock(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	Expect(err).ToNot(HaveOccurred())
	mr, err := core.NewModelRegistryService(grpcConn)
	Expect(err).ToNot(HaveOccurred())

	return mr
}

// undeployModelRegistry cleanup model registry deployments
func undeployModelRegistry() {
	cmd := exec.Command("kubectl", "delete", "-f", ModelRegistryDeploymentPath)
	_, err = utils.Run(cmd)
	Expect(err).ToNot(HaveOccurred())

	cmd = exec.Command("kubectl", "delete", "-f", ModelRegistryDatabaseDeploymentPath)
	_, err = utils.Run(cmd)
	Expect(err).ToNot(HaveOccurred())
}

// waitForModelRegistryStartup checks and block the execution until model registry is up and running
func waitForModelRegistryStartup() {
	By("by checking that the model registry database is up and running")
	Eventually(func() bool {
		opts := []client.ListOption{client.InNamespace(WorkingNamespace), client.MatchingLabels{
			"name": "model-registry-db",
		}}
		podList := &corev1.PodList{}
		err := cli.List(ctx, podList, opts...)
		if err != nil || len(podList.Items) == 0 {
			return false
		}
		return getPodReadyCondition(&podList.Items[0])
	}, timeout, interval).Should(BeTrue())

	By("by checking that the model registry proxy and mlmd is up and running")
	Eventually(func() bool {
		opts := []client.ListOption{client.InNamespace(WorkingNamespace), client.MatchingLabels{
			"component": "model-registry",
		}}
		podList := &corev1.PodList{}
		err := cli.List(ctx, podList, opts...)
		if err != nil || len(podList.Items) == 0 {
			return false
		}
		return getPodReadyCondition(&podList.Items[0])
	}, timeout, interval).Should(BeTrue())
}

// getPodReadyCondition retrieves the Pod ready condition as bool
func getPodReadyCondition(pod *corev1.Pod) bool {
	for _, c := range pod.Status.Conditions {
		// fmt.Fprintf(GinkgoWriter, "Checking %s = %v\n", c.Type, c.Status)
		if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func fillModelRegistryContent(mr api.ModelRegistryApi) (*openapi.RegisteredModel, *openapi.ModelVersion, *openapi.ModelArtifact) {
	model, err := mr.GetRegisteredModelByParams(&modelName, nil)
	if err != nil {
		// register a new model
		model, err = mr.UpsertRegisteredModel(&openapi.RegisteredModel{
			Name: &modelName,
		})
		Expect(err).ToNot(HaveOccurred())
	}

	version, err := mr.GetModelVersionByParams(&versionName, model.Id, nil)
	if err != nil {
		version, err = mr.UpsertModelVersion(&openapi.ModelVersion{
			Name: &versionName,
		}, model.Id)
		Expect(err).ToNot(HaveOccurred())
	}

	modelArtifactName := fmt.Sprintf("%s-artifact", versionName)
	artifact, err := mr.GetModelArtifactByParams(&modelArtifactName, version.Id, nil)
	if err != nil {
		artifact, err = mr.UpsertModelArtifact(&openapi.ModelArtifact{
			Name:               &modelArtifactName,
			ModelFormatName:    &modelFormatName,
			ModelFormatVersion: &modelFormatVersion,
			StorageKey:         &storageKey,
			StoragePath:        &storagePath,
		}, version.Id)
		Expect(err).ToNot(HaveOccurred())
	}

	return model, version, artifact
}
