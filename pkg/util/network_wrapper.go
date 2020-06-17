package util

import (
	"errors"
	"fmt"

	nettypes "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"

	networkclient "kubevirt.io/client-go/generated/network-attachment-definition-client/clientset/versioned"
	"kubevirt.io/client-go/kubecli"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	DefaultNs string = "default"
)

type ClientInfo struct {
	//Client    kubernetes.Interface
	NetClient networkclient.Interface //
	//EventBroadcaster record.EventBroadcaster
	//EventRecorder    record.EventRecorder
}

func NewClientInfo() *ClientInfo {
	config := "/opt/cisco/cluster-config"
	c, err := kubecli.GetKubevirtClientFromFlags("", config)
	if err != nil {
		fmt.Printf("\nerror: %v, Can't open %v", err, config)
		return nil
	}
	// clientset, err := kubernetes.NewForConfig(config)
	// if err != nil {
	// 	fmt.Printf("Failed to create client: %v\n", err)
	// 	return nil
	// }

	netclient := c.NetworkClient()
	return &ClientInfo{
		//Client:    clientset,
		NetClient: netclient,
	}
}

func NewNetAttachDef(namespace, name, config string) (*nettypes.NetworkAttachmentDefinition, error) {
	if name == "" || config == "" {
		return nil, errors.New("Name and config of Network-Attachment-Definition are required")
	}
	if namespace == "" {
		namespace = DefaultNs
	}
	return &nettypes.NetworkAttachmentDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: nettypes.NetworkAttachmentDefinitionSpec{
			Config: config,
		},
	}, nil
}

func ListNetworkAttachmentDefinition(ns string) (*nettypes.NetworkAttachmentDefinitionList, error) {
	return NewClientInfo().ListNetworkAttachmentDefinition(ns)
}

func (c *ClientInfo) ListNetworkAttachmentDefinition(ns string) (*nettypes.NetworkAttachmentDefinitionList, error) {
	attachments, err := c.NetClient.K8sCniCncfIoV1().NetworkAttachmentDefinitions(ns).List(metav1.ListOptions{})
	if err != nil {
		fmt.Printf("Failed to get attachements: %+v\n", err)
		return nil, err
	}

	return attachments, nil
}

// AddNetAttachDef adds net-attach-def into kubernetes
func AddNetAttachDef(netattach *nettypes.NetworkAttachmentDefinition) (*nettypes.NetworkAttachmentDefinition, error) {
	return NewClientInfo().AddNetAttachDef(netattach)
}
func (c *ClientInfo) AddNetAttachDef(netattach *nettypes.NetworkAttachmentDefinition) (*nettypes.NetworkAttachmentDefinition, error) {
	return c.NetClient.K8sCniCncfIoV1().NetworkAttachmentDefinitions(netattach.ObjectMeta.Namespace).Create(netattach)
}

func UpdateNetAttachDef(netattach *nettypes.NetworkAttachmentDefinition) (*nettypes.NetworkAttachmentDefinition, error) {
	return NewClientInfo().UpdateNetAttachDef(netattach)
}
func (c *ClientInfo) UpdateNetAttachDef(netattach *nettypes.NetworkAttachmentDefinition) (*nettypes.NetworkAttachmentDefinition, error) {
	old, err := c.NetClient.K8sCniCncfIoV1().NetworkAttachmentDefinitions(netattach.ObjectMeta.Namespace).Get(netattach.ObjectMeta.Name, metav1.GetOptions{})
	if err != nil {
		fmt.Println(err)
		return nil, err
	}
	netattach.ObjectMeta.ResourceVersion = old.ObjectMeta.ResourceVersion
	return c.NetClient.K8sCniCncfIoV1().NetworkAttachmentDefinitions(netattach.ObjectMeta.Namespace).Update(netattach)
}

func DeleteNetAttachDef(ns string, name string) error {
	return NewClientInfo().DeleteNetAttachDef(ns, name)
}
func (c *ClientInfo) DeleteNetAttachDef(ns string, name string) error {
	return c.NetClient.K8sCniCncfIoV1().NetworkAttachmentDefinitions(ns).Delete(name, &metav1.DeleteOptions{})
}

/*

const (
	DefaultNs string = "default"
)

func NewNetAttachDef(namespace, name, config string) (*nettypes.NetworkAttachmentDefinition, error) {
	if name == "" || config == "" {
		return nil, errors.New("Name and config of Network-Attachment-Definition are required")
	}
	if namespace == "" {
		namespace = DefaultNs
	}
	return &nettypes.NetworkAttachmentDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: nettypes.NetworkAttachmentDefinitionSpec{
			Config: config,
		},
	}, nil
}

func ListNetworkAttachmentDefinition(c kubecli.KubevirtClient, ns string) (*nettypes.NetworkAttachmentDefinitionList, error) {
	attachments, err := c.NetworkClient().K8sCniCncfIoV1().NetworkAttachmentDefinitions(ns).List(metav1.ListOptions{})
	if err != nil {
		fmt.Printf("Failed to get attachments: %+v\n", err)
		return nil, err
	}

	return attachments, nil
}

// AddNetAttachDef adds net-attach-def into kubernetes
func AddNetAttachDef(c kubecli.KubevirtClient, netattach *nettypes.NetworkAttachmentDefinition) (*nettypes.NetworkAttachmentDefinition, error) {
	return c.NetworkClient().K8sCniCncfIoV1().NetworkAttachmentDefinitions(netattach.ObjectMeta.Namespace).Create(netattach)
}

func UpdateNetAttachDef(c kubecli.KubevirtClient, netattach *nettypes.NetworkAttachmentDefinition) (*nettypes.NetworkAttachmentDefinition, error) {
	old, err := c.NetworkClient().K8sCniCncfIoV1().NetworkAttachmentDefinitions(netattach.ObjectMeta.Namespace).Get(netattach.ObjectMeta.Name, metav1.GetOptions{})
	if err != nil {
		fmt.Println(err)
		return nil, err
	}
	netattach.ObjectMeta.ResourceVersion = old.ObjectMeta.ResourceVersion
	return c.NetworkClient().K8sCniCncfIoV1().NetworkAttachmentDefinitions(netattach.ObjectMeta.Namespace).Update(netattach)
}

func DeleteNetAttachDef(c kubecli.KubevirtClient, ns string, name string) error {
	return c.NetworkClient().K8sCniCncfIoV1().NetworkAttachmentDefinitions(ns).Delete(name, &metav1.DeleteOptions{})
}

*/
