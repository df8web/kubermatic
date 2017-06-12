package cluster

import (
	"fmt"
	"path"
	"time"

	"github.com/cloudflare/cfssl/csr"
	"github.com/cloudflare/cfssl/initca"
	"github.com/golang/glog"
	"github.com/kubermatic/api"
	"github.com/kubermatic/api/controller/resources"
	"github.com/kubermatic/api/controller/template"
	"github.com/kubermatic/api/extensions"
	"github.com/kubermatic/api/provider/kubernetes"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/pkg/api/v1"
	extensionsv1beta1 "k8s.io/client-go/pkg/apis/extensions/v1beta1"
	"k8s.io/client-go/pkg/apis/rbac/v1beta1"
)

func (cc *clusterController) syncPendingCluster(c *api.Cluster) (changedC *api.Cluster, err error) {
	_, err = cc.checkTimeout(c)
	if err != nil {
		return nil, err
	}

	changedC, err = cc.pendingCreateRootCA(c)
	if err != nil || changedC != nil {
		return changedC, err
	}

	// create token-users first and also persist immediately because this
	// changes the cluster. The later secrets and other resources don't.
	changedC, err = cc.launchingCheckTokenUsers(c)
	if err != nil || changedC != nil {
		return changedC, err
	}

	// create apiservers public service early to have valid contact information
	changedC, err = cc.launchingCheckApiserverPublicService(c)
	if err != nil || changedC != nil {
		return changedC, err
	}

	// check that all service accounts are created
	err = cc.launchingCheckServiceAccounts(c)
	if err != nil {
		return changedC, err
	}

	// check that all role bindings are created
	err = cc.launchingCheckClusterRoleBindings(c)
	if err != nil {
		return changedC, err
	}

	// check that all services are available
	changedC, err = cc.launchingCheckServices(c)
	if err != nil || changedC != nil {
		return changedC, err
	}

	changedC, err = cc.pendingCheckSecrets(c)
	if err != nil || changedC != nil {
		return changedC, err
	}

	err = cc.launchingCheckConfigMaps(c)
	if err != nil || changedC != nil {
		return changedC, err
	}

	////check that all pvc's are available
	err = cc.launchingCheckPvcs(c)
	if err != nil {
		return nil, err
	}

	// check that all deployments are available
	changedC, err = cc.launchingCheckDeployments(c)
	if err != nil {
		return changedC, err
	}

	// check that all deployments are available
	changedC, err = cc.launchingCheckEtcdCluster(c)
	if err != nil {
		return changedC, err
	}

	err = cc.launchingCheckDefaultPlugins(c)
	if err != nil {
		return nil, err
	}

	c.Status.LastTransitionTime = time.Now()
	c.Status.Phase = api.LaunchingClusterStatusPhase
	return c, nil
}

func (cc *clusterController) pendingCreateRootCA(c *api.Cluster) (*api.Cluster, error) {
	if c.Status.RootCA.Key != nil {
		return nil, nil
	}

	rootCAReq := csr.CertificateRequest{
		CN: fmt.Sprintf("root-ca.%s.%s.%s", c.Metadata.Name, cc.dc, cc.externalURL),
		KeyRequest: &csr.BasicKeyRequest{
			A: "rsa",
			S: 2048,
		},
		CA: &csr.CAConfig{
			Expiry: fmt.Sprintf("%dh", 24*365*10),
		},
	}
	var err error
	c.Status.RootCA.Cert, _, c.Status.RootCA.Key, err = initca.New(&rootCAReq)
	if err != nil {
		return nil, fmt.Errorf("failed to create root-ca: %v", err)
	}

	return c, nil
}

func (cc *clusterController) pendingCheckSecrets(c *api.Cluster) (*api.Cluster, error) {
	secrets := map[string]func(cc *clusterController, c *api.Cluster, t *template.Template) (*api.Cluster, *v1.Secret, error){
		"apiserver-auth": createApiserverAuth,
		"apiserver-ssh":  createApiserverSSH,
	}

	recreateSecrets := map[string]struct{}{}
	if len(c.Status.ApiserverSSH) == 0 {
		recreateSecrets["apiserver-ssh"] = struct{}{}
	}

	ns := kubernetes.NamespaceName(c.Metadata.Name)
	for s, gen := range secrets {
		key := fmt.Sprintf("%s/%s", ns, s)
		_, exists, err := cc.secretStore.GetByKey(key)
		if err != nil {
			return nil, err
		}
		if exists {
			if _, recreate := recreateSecrets[s]; !recreate {
				glog.V(6).Infof("Skipping already existing secret %q", key)
				continue
			}

			err = cc.client.CoreV1().Secrets(ns).Delete(s, &metav1.DeleteOptions{})
			if err != nil {
				return nil, err
			}
		}

		t, err := template.ParseFiles(path.Join(cc.masterResourcesPath, s+"-secret.yaml"))
		if err != nil {
			return nil, err
		}

		changedC, secret, err := gen(cc, c, t)
		if err != nil {
			return nil, fmt.Errorf("failed to generate %s: %v", s, err)
		}

		_, err = cc.client.CoreV1().Secrets(ns).Create(secret)
		if err != nil {
			return nil, fmt.Errorf("failed to create secret for %s: %v", s, err)
		}

		cc.recordClusterEvent(c, "pending", "Created secret %q", key)

		if changedC != nil {
			return changedC, nil
		}
	}

	return nil, nil
}

func (cc *clusterController) launchingCheckTokenUsers(c *api.Cluster) (*api.Cluster, error) {
	ns := kubernetes.NamespaceName(c.Metadata.Name)

	key := fmt.Sprintf("%s/token-users", ns)
	_, exists, err := cc.secretStore.GetByKey(key)
	if err != nil {
		return nil, err
	}
	if exists {
		glog.V(6).Infof("Skipping already existing secret %q", key)
		return nil, nil
	}

	secret, err := createTokens(c)
	if err != nil {
		return nil, fmt.Errorf("failed to generate token users: %v", err)
	}
	_, err = cc.client.CoreV1().Secrets(ns).Create(secret)
	if err != nil {
		return nil, fmt.Errorf("failed to create secret for token user: %v", err)
	}
	cc.recordClusterEvent(c, "launching", "Created secret %q", key)
	return c, nil
}

func (cc *clusterController) GetFreeNodePort() (int, error) {
	services := cc.serviceStore.List()

	usedPorts := []int{}
	for _, s := range services {
		service := s.(*v1.Service)
		for _, port := range service.Spec.Ports {
			if port.NodePort == 0 {
				continue
			}
			usedPorts = append(usedPorts, int(port.NodePort))
		}
	}

	isIn := func(p int, takenPorts []int) bool {
		for _, takenPort := range takenPorts {
			if p == takenPort {
				return true
			}
		}
		return false
	}

	port := cc.minAPIServerPort
	for port <= cc.maxAPIServerPort {
		if isIn(port, usedPorts) {
			port++
			continue
		}
		return port, nil
	}

	return 0, fmt.Errorf("no free NodePort available within the given range %d-%d", cc.minAPIServerPort, cc.maxAPIServerPort)
}

func (cc *clusterController) launchingCheckApiserverPublicService(c *api.Cluster) (*api.Cluster, error) {
	ns := kubernetes.NamespaceName(c.Metadata.Name)
	key := fmt.Sprintf("%s/%s", ns, "apiserver")
	_, exists, err := cc.serviceStore.GetByKey(key)
	if err != nil {
		return nil, err
	}

	if exists {
		return nil, nil
	}

	c.Address.ApiserverExternalPort, err = cc.GetFreeNodePort()
	if err != nil {
		return nil, err
	}
	c.Address.URL = fmt.Sprintf("https://%s.%s.%s:%d", c.Metadata.Name, cc.dc, cc.externalURL, c.Address.ApiserverExternalPort)

	service, err := resources.LoadServiceFile(c, "apiserver", cc.masterResourcesPath)
	if err != nil {
		return nil, fmt.Errorf("failed to generate apiserver service %s: %v", key, err)
	}

	service, err = cc.client.CoreV1().Services(ns).Create(service)
	if err != nil {
		return nil, fmt.Errorf("failed to create apiserver service %s: %v", key, err)
	}

	cc.recordClusterEvent(c, "launching", "Created apiserver service %q", key)

	return c, nil
}
func (cc *clusterController) launchingCheckServices(c *api.Cluster) (*api.Cluster, error) {
	services := map[string]func(c *api.Cluster, app, masterResourcesPath string) (*v1.Service, error){
		"apiserver-insecure": resources.LoadServiceFile,
	}

	ns := kubernetes.NamespaceName(c.Metadata.Name)
	for s, gen := range services {
		key := fmt.Sprintf("%s/%s", ns, s)
		_, exists, err := cc.serviceStore.GetByKey(key)
		if err != nil {
			return nil, err
		}

		if exists {
			glog.V(6).Infof("Skipping already existing service %q", key)
			continue
		}

		service, err := gen(c, s, cc.masterResourcesPath)
		if err != nil {
			return nil, fmt.Errorf("failed to generate service %s: %v", s, err)
		}

		_, err = cc.client.CoreV1().Services(ns).Create(service)
		if err != nil {
			return nil, fmt.Errorf("failed to create service %s: %v", s, err)
		}

		cc.recordClusterEvent(c, "launching", "Created service %q", s)
	}

	return nil, nil
}

func (cc *clusterController) launchingCheckServiceAccounts(c *api.Cluster) error {
	serviceAccounts := map[string]func(app, masterResourcesPath string) (*v1.ServiceAccount, error){
		"etcd-operator": resources.LoadServiceAccountFile,
	}

	ns := kubernetes.NamespaceName(c.Metadata.Name)
	for s, gen := range serviceAccounts {
		key := fmt.Sprintf("%s/%s", ns, s)
		_, exists, err := cc.saStore.GetByKey(key)
		if err != nil {
			return err
		}

		if exists {
			glog.V(6).Infof("Skipping already existing service account %q", key)
			continue
		}

		sa, err := gen(s, cc.masterResourcesPath)
		if err != nil {
			return fmt.Errorf("failed to generate service account %s: %v", s, err)
		}

		_, err = cc.client.CoreV1().ServiceAccounts(ns).Create(sa)
		if err != nil {
			return fmt.Errorf("failed to create service account %s: %v", s, err)
		}

		cc.recordClusterEvent(c, "launching", "Created service account %q", s)
	}

	return nil
}

func (cc *clusterController) launchingCheckClusterRoleBindings(c *api.Cluster) error {
	roleBindings := map[string]func(namespace, app, masterResourcesPath string) (*v1beta1.ClusterRoleBinding, error){
		"etcd-operator": resources.LoadClusterRoleBindingFile,
	}

	ns := kubernetes.NamespaceName(c.Metadata.Name)
	for s, gen := range roleBindings {
		binding, err := gen(ns, s, cc.masterResourcesPath)
		if err != nil {
			return fmt.Errorf("failed to generate cluster role binding %s: %v", s, err)
		}

		_, exists, err := cc.clusterRoleBindingStore.GetByKey(binding.ObjectMeta.Name)
		if err != nil {
			return err
		}

		if exists {
			glog.V(6).Infof("Skipping already existing cluster role binding %q", binding.ObjectMeta.Name)
			continue
		}

		_, err = cc.client.RbacV1beta1().ClusterRoleBindings().Create(binding)
		if err != nil {
			return fmt.Errorf("failed to create cluster role binding %s: %v", s, err)
		}

		cc.recordClusterEvent(c, "launching", "Created binding %q", s)
	}

	return nil
}

func (cc *clusterController) launchingCheckDeployments(c *api.Cluster) (*api.Cluster, error) {
	ns := kubernetes.NamespaceName(c.Metadata.Name)

	if c.Spec.MasterVersion == "" {
		c.Spec.MasterVersion = cc.defaultMasterVersion.ID
	}
	masterVersion, found := cc.versions[c.Spec.MasterVersion]
	if !found {
		c.Status.LastTransitionTime = time.Now()
		c.Status.Phase = api.FailedClusterStatusPhase
		glog.Warningf("Unknown new cluster %q master version %q", c.Metadata.Name, c.Spec.MasterVersion)
		cc.recordClusterEvent(c, "launching", "Failed to create new cluster %q due to unknown master version %q", c.Metadata.Name, c.Spec.MasterVersion)
		return c, fmt.Errorf("unknown new cluster %q master version %q", c.Metadata.Name, c.Spec.MasterVersion)
	}

	deps := map[string]string{
		"etcd-operator":      masterVersion.EtcdOperatorDeploymentYaml,
		"apiserver":          masterVersion.ApiserverDeploymentYaml,
		"controller-manager": masterVersion.ControllerDeploymentYaml,
		"scheduler":          masterVersion.SchedulerDeploymentYaml,
	}

	existingDeps, err := cc.depStore.ByIndex("namespace", ns)
	if err != nil {
		return nil, err
	}

	for s, yamlFile := range deps {
		exists := false
		for _, obj := range existingDeps {
			dep := obj.(*extensionsv1beta1.Deployment)
			if role, found := dep.Spec.Selector.MatchLabels["role"]; found && role == s {
				exists = true
				break
			}
		}
		if exists {
			glog.V(7).Infof("Skipping already existing dep %q for cluster %q", s, c.Metadata.Name)
			continue
		}

		dep, err := resources.LoadDeploymentFile(c, masterVersion, cc.masterResourcesPath, cc.dc, yamlFile)
		if err != nil {
			return nil, fmt.Errorf("failed to generate deployment %s: %v", s, err)
		}

		_, err = cc.client.ExtensionsV1beta1().Deployments(ns).Create(dep)
		if err != nil {
			return nil, fmt.Errorf("failed to create deployment %s: %v", s, err)
		}

		cc.recordClusterEvent(c, "launching", "Created dep %q", s)
	}

	return nil, nil
}

func (cc *clusterController) launchingCheckConfigMaps(c *api.Cluster) error {
	ns := kubernetes.NamespaceName(c.Metadata.Name)

	cms := map[string]func(c *api.Cluster) (*v1.ConfigMap, error){}
	if c.Spec.Cloud != nil && c.Spec.Cloud.AWS != nil {
		cms["aws-cloud-config"] = resources.LoadAwsCloudConfigConfigMap
	}

	for s, gen := range cms {
		key := fmt.Sprintf("%s/%s", ns, s)
		_, exists, err := cc.cmStore.GetByKey(key)
		if err != nil {
			return err
		}

		if exists {
			glog.V(6).Infof("Skipping already existing cm %q", key)
			continue
		}

		cm, err := gen(c)
		if err != nil {
			return fmt.Errorf("failed to generate cm %s: %v", s, err)
		}

		_, err = cc.client.CoreV1().ConfigMaps(ns).Create(cm)
		if err != nil {
			return fmt.Errorf("failed to create cm %s; %v", s, err)
		}

		cc.recordClusterEvent(c, "launching", "Created cm %q", s)
	}

	return nil
}

func (cc *clusterController) launchingCheckPvcs(c *api.Cluster) error {
	ns := kubernetes.NamespaceName(c.Metadata.Name)

	pvcs := map[string]func(c *api.Cluster, app, masterResourcesPath string) (*v1.PersistentVolumeClaim, error){
	// Currently not required pvc for etcd is done by etcd-operator
	// TODO launchingCheckPvcs can be removed in the future if we don't need PVC in general
	//	"etcd": resources.LoadPVCFile,
	}

	for s, gen := range pvcs {
		key := fmt.Sprintf("%s/%s-pvc", ns, s)
		_, exists, err := cc.pvcStore.GetByKey(key)
		if err != nil {
			return err
		}

		if exists {
			glog.V(6).Infof("Skipping already existing pvc %q", key)
			continue
		}

		pvc, err := gen(c, s, cc.masterResourcesPath)
		if err != nil {
			return fmt.Errorf("failed to generate pvc %s: %v", s, err)
		}

		_, err = cc.client.CoreV1().PersistentVolumeClaims(ns).Create(pvc)
		if err != nil {
			return fmt.Errorf("failed to create pvc %s; %v", s, err)
		}

		cc.recordClusterEvent(c, "launching", "Created pvc %q", s)
	}

	return nil
}

func (cc *clusterController) launchingCheckDefaultPlugins(c *api.Cluster) error {
	ns := kubernetes.NamespaceName(c.Metadata.Name)
	defaultPlugins := map[string]string{
		"flannelcni":          "flannel-cni",
		"heapster":            "heapster",
		"kubedns":             "kubedns",
		"kubeproxy":           "kube-proxy",
		"kubernetesdashboard": "kubernetes-dashboard",
	}

	for safeName, name := range defaultPlugins {
		metaName := fmt.Sprintf("addon-default-%s", safeName)
		_, exists, err := cc.addonStore.GetByKey(fmt.Sprintf("%s/%s", ns, metaName))
		if err != nil {
			return err
		}

		if exists {
			glog.V(6).Infof("Skipping already existing default addon %q", metaName)
			continue
		}

		addon := &extensions.ClusterAddon{
			Metadata: metav1.ObjectMeta{
				Name: metaName,
			},
			Name:  name,
			Phase: extensions.PendingAddonStatusPhase,
		}

		_, err = cc.tprClient.ClusterAddons(fmt.Sprintf("cluster-%s", c.Metadata.Name)).Create(addon)
		if err != nil {
			return fmt.Errorf("failed to create default addon third-party-resource %s; %v", name, err)
		}
	}

	return nil
}

func (cc *clusterController) launchingCheckEtcdCluster(c *api.Cluster) (*api.Cluster, error) {
	ns := kubernetes.NamespaceName(c.Metadata.Name)
	if c.Spec.MasterVersion == "" {
		c.Spec.MasterVersion = cc.defaultMasterVersion.ID
	}
	masterVersion, found := cc.versions[c.Spec.MasterVersion]
	if !found {
		c.Status.LastTransitionTime = time.Now()
		c.Status.Phase = api.FailedClusterStatusPhase
		glog.Warningf("Unknown new cluster %q master version %q", c.Metadata.Name, c.Spec.MasterVersion)
		cc.recordClusterEvent(c, "launching", "Failed to create new cluster %q due to unknown master version %q", c.Metadata.Name, c.Spec.MasterVersion)
		return c, fmt.Errorf("unknown new cluster %q master version %q", c.Metadata.Name, c.Spec.MasterVersion)
	}

	etcd, err := resources.LoadEtcdClusterFile(masterVersion, cc.masterResourcesPath, masterVersion.EtcdClusterYaml)
	if err != nil {
		return nil, fmt.Errorf("failed to load etcd-cluster: %v", err)
	}

	key := fmt.Sprintf("%s/%s", ns, etcd.Metadata.Name)
	_, exists, err := cc.etcdClusterStore.GetByKey(key)
	if err != nil {
		return nil, err
	}

	if exists {
		glog.V(7).Infof("Skipping already existing etcd-cluster for cluster %q", c.Metadata.Name)
		return c, nil
	}

	_, err = cc.etcdClusterClient.Cluster(ns).Create(etcd)
	if err != nil {
		return nil, fmt.Errorf("failed to create ecd-cluster definition (tpr): %v", err)
	}

	cc.recordClusterEvent(c, "launching", "Created etcd-cluster", etcd.Metadata.Name)

	return nil, nil
}
