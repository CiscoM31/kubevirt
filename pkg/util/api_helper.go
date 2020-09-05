/*
 API Extensions to enable external clients to orchestrate kubevirt VMs
*/
package util

import (
	"encoding/json"
	errors2 "errors"
	"fmt"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	k8sv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	apierrs "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/clientcmd"

	networkv1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"

	v1 "kubevirt.io/client-go/api/v1"
	cdiClientset "kubevirt.io/client-go/generated/containerized-data-importer/clientset/versioned"
	"kubevirt.io/client-go/kubecli"
	cdiv1 "kubevirt.io/containerized-data-importer/pkg/apis/core/v1alpha1"
	"encoding/base64"
)

type DiskParams struct {
	Name, VolName, Class, Bus string
	Svolume, SfilePath        string
	SourceCerts               string
	Evolume, EfilePath        string
	DataDisk                  string
	Capacity, Format          string
	VolumeHandle              string
	IsCdrom                   bool
	VolumeBlockMode           bool
	CloneFromDisk             string
	HxVolName                 string
	Order                     uint
	AccessMode                string
	NameSpace                 string
}

const (
	pvcCreatedByVM = "vmCreated"
	pvcCDICreated  = "cdi.kubevirt.io/storage.import.source"
	CloudInitName  = "cloudinitdisk"
	DefaultTLSCert  = "default-tls-cert"
)

func NewVM(namespace string, vmName string) *v1.VirtualMachine {
	vm := &v1.VirtualMachine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      strings.ToLower(vmName),
			Namespace: namespace,
		},
		Spec: v1.VirtualMachineSpec{
			Template: &v1.VirtualMachineInstanceTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:    map[string]string{"name": vmName},
					Name:      strings.ToLower(vmName),
					Namespace: namespace,
				},
			},
		},
	}
	vm.SetGroupVersionKind(schema.GroupVersionKind{Group: v1.GroupVersion.Group, Kind: "VirtualMachine", Version: v1.GroupVersion.Version})
	return vm
}

func NewReplicaSetFromVMI(vmi *v1.VirtualMachineInstance, replicas int32) *v1.VirtualMachineInstanceReplicaSet {
	name := "replicaset" + rand.String(5)
	rs := &v1.VirtualMachineInstanceReplicaSet{
		ObjectMeta: metav1.ObjectMeta{Name: "replicaset" + rand.String(5)},
		Spec: v1.VirtualMachineInstanceReplicaSetSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"name": name},
			},
			Template: &v1.VirtualMachineInstanceTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"name": name},
					Name:   vmi.ObjectMeta.Name,
				},
				Spec: vmi.Spec,
			},
		},
	}
	return rs
}

// Create a CSI PV based on an existing volume handle. Should
// be used only for a case where the disks are being recovered from a
// lost Kubernetes install. The volume Handle from previous creation
// will enable us to recover this disk from StorFS
func CreateCSIPv(client kubecli.KubevirtClient, name string, params DiskParams) (*k8sv1.PersistentVolume, error) {
	quantity, err := resource.ParseQuantity(params.Capacity)
	if err != nil {
		return nil, err
	}

	accessMode := VDiskAccessModetoPVAccessMode(params.AccessMode)

	mode := k8sv1.PersistentVolumeFilesystem
	if params.VolumeBlockMode {
		mode = k8sv1.PersistentVolumeBlock
	}
	pv := &k8sv1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: k8sv1.PersistentVolumeSpec{
			AccessModes: accessMode,
			Capacity: k8sv1.ResourceList{
				"storage": quantity,
			},
			PersistentVolumeReclaimPolicy: k8sv1.PersistentVolumeReclaimRetain,
			StorageClassName:              params.Class,
			VolumeMode:                    &mode,
		},
	}
	pv.Annotations = make(map[string]string, 0)
	pv.Annotations["pv.kubernetes.io/provisioned-by"] = "csi-hxcsi"
	csi := k8sv1.CSIPersistentVolumeSource{}
	csi.Driver = "csi-hxcsi"
	if !params.VolumeBlockMode {
		csi.FSType = "ext4"
	}
	csi.VolumeHandle = params.VolumeHandle

	pv.Spec.PersistentVolumeSource.CSI = &csi

	pv1, err := client.CoreV1().PersistentVolumes().Create(pv)
	if errors.IsAlreadyExists(err) {
		return pv1, nil
	}
	return pv1, err
}

func CreateISCSIPv(client kubecli.KubevirtClient, name, class, capacity, target, iqn string, lun int32,
	readOnly, volBlockMode bool) (*k8sv1.PersistentVolume, error) {
	quantity, err := resource.ParseQuantity(capacity)
	if err != nil {
		return nil, err
	}

	accessMode := k8sv1.ReadWriteMany
	if readOnly {
		accessMode = k8sv1.ReadOnlyMany
	}
	fmt.Printf("Access mode: %v", accessMode)

	mode := k8sv1.PersistentVolumeFilesystem
	if volBlockMode {
		mode = k8sv1.PersistentVolumeBlock
	}
	pv := &k8sv1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: k8sv1.PersistentVolumeSpec{
			AccessModes: []k8sv1.PersistentVolumeAccessMode{accessMode},
			Capacity: k8sv1.ResourceList{
				"storage": quantity,
			},
			PersistentVolumeReclaimPolicy: k8sv1.PersistentVolumeReclaimRetain,
			PersistentVolumeSource: k8sv1.PersistentVolumeSource{
				ISCSI: &k8sv1.ISCSIPersistentVolumeSource{
					TargetPortal: target,
					IQN:          iqn,
					Lun:          lun,
					ReadOnly:     readOnly,
				},
			},
			StorageClassName: class,
			VolumeMode:       &mode,
		},
	}

	pv1, err := client.CoreV1().PersistentVolumes().Create(pv)
	if errors.IsAlreadyExists(err) {
		return pv1, nil
	}
	return pv1, err
}

func CreateISCSIPvc(client kubecli.KubevirtClient, name, vName, class, capacity, ns string, readOnly, volumeBlockMode bool,
	annot map[string]string) (*k8sv1.PersistentVolumeClaim, error) {
	quantity, err := resource.ParseQuantity(capacity)
	if err != nil {
		return nil, err
	}
	mode := k8sv1.PersistentVolumeFilesystem
	if volumeBlockMode {
		mode = k8sv1.PersistentVolumeBlock
	}

	accessMode := k8sv1.ReadWriteMany
	if readOnly {
		accessMode = k8sv1.ReadOnlyMany
	}

	pvc := &k8sv1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: name, Annotations: annot},
		Spec: k8sv1.PersistentVolumeClaimSpec{
			AccessModes: []k8sv1.PersistentVolumeAccessMode{accessMode},
			Resources: k8sv1.ResourceRequirements{
				Requests: k8sv1.ResourceList{
					"storage": quantity,
				},
			},
			StorageClassName: &class,
			VolumeMode:       &mode,
		},
	}

	pvc.Spec.VolumeName = vName
	_, err = client.CoreV1().PersistentVolumeClaims(ns).Create(pvc)
	if errors.IsAlreadyExists(err) {
		return pvc, nil
	}

	return pvc, err
}

func GetPVNamefromPVC(c kubecli.KubevirtClient, pvcName, ns string) (string, error) {
	pvc, err := c.CoreV1().PersistentVolumeClaims(ns).Get(pvcName, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	return pvc.Spec.VolumeName, nil
}

func ListPVs(client kubecli.KubevirtClient) (*k8sv1.PersistentVolumeList, error) {
	return client.CoreV1().PersistentVolumes().List(metav1.ListOptions{})
}

func ListPVCs(client kubecli.KubevirtClient) (*k8sv1.PersistentVolumeClaimList, error) {
	return client.CoreV1().PersistentVolumeClaims("default").List(metav1.ListOptions{})
}

func DeletePV(client kubecli.KubevirtClient, name string) error {
	return client.CoreV1().PersistentVolumes().Delete(name, &metav1.DeleteOptions{})
}

func updatePVCPrivate(client kubecli.KubevirtClient, pvc *k8sv1.PersistentVolumeClaim) (*k8sv1.PersistentVolumeClaim, error) {
	pvc.Spec.AccessModes = []k8sv1.PersistentVolumeAccessMode{k8sv1.ReadWriteMany}
	return client.CoreV1().PersistentVolumeClaims(pvc.Namespace).Update(pvc)
}

func SetResources(vm *v1.VirtualMachine, cpu, memory, cpuModel string) error {
	t := int64(30)
	vmSpec := &vm.Spec.Template.Spec
	vmSpec.TerminationGracePeriodSeconds = &t
	cpus := resource.MustParse(cpu)
	vmSpec.Domain.Resources.Requests = make(map[k8sv1.ResourceName]resource.Quantity, 0)
	vmSpec.Domain.Resources.Requests[k8sv1.ResourceMemory] = resource.MustParse(memory)
	vmSpec.Domain.Resources.Requests[k8sv1.ResourceCPU] = cpus
	cores, err := strconv.Atoi(cpu)
	if err != nil {
		return fmt.Errorf("SetResources failed: %v", err)
	}
	fmt.Printf("\nCores: %d\n", cores)
	vmSpec.Domain.CPU = &v1.CPU{Cores: uint32(cores), Model: cpuModel}

	vmSpec.Domain.CPU = &v1.CPU{
		Cores: uint32(cores),
		Model: cpuModel,
		Features: []v1.CPUFeature{
			{
				Name:   "vmx",
				Policy: "disable",
			},
		},
	}
	return nil
}

func VDiskAccessModetoPVAccessMode(mode string) []k8sv1.PersistentVolumeAccessMode {
	switch mode {
	case "ReadWriteOnce":
		return []k8sv1.PersistentVolumeAccessMode{k8sv1.ReadWriteOnce}
	case "ReadWriteMany":
		return []k8sv1.PersistentVolumeAccessMode{k8sv1.ReadWriteMany}
	case "ReadOnlyMany":
		return []k8sv1.PersistentVolumeAccessMode{k8sv1.ReadOnlyMany}
	}
	return []k8sv1.PersistentVolumeAccessMode{k8sv1.ReadWriteOnce}
}

func CreateCertConfigMap(client kubecli.KubevirtClient, certMap *k8sv1.ConfigMap) error {
	if certMap == nil {
		return nil
	}
	_, err := client.CoreV1().ConfigMaps(certMap.Namespace).Create(certMap)
	if errors.IsAlreadyExists(err) {
		_, err = client.CoreV1().ConfigMaps(certMap.Namespace).Update(certMap)
	}
	return err
}

func DeleteCertConfigMap(c kubecli.KubevirtClient, name, ns string) error {
	return c.CoreV1().ConfigMaps(ns).Delete(name, &metav1.DeleteOptions{})
}

func NewCertConfigMap(params *DiskParams) *k8sv1.ConfigMap {
	if params.SourceCerts != "" {
		cert, err := base64.StdEncoding.DecodeString(params.SourceCerts)
		if err != nil {
			return  nil
		}
		return &k8sv1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: params.NameSpace,
				Name:      strings.ToLower(params.Name),
			},
			Data: map[string]string{
				"ca.pem": string(cert),
			},
		}
	}
	return nil
}

// NewDataVolumeWithHTTPImport initializes a DataVolume struct with HTTP annotations
func NewDataVolumeWithHTTPImport(params *DiskParams) *cdiv1.DataVolume {
	accessMode := VDiskAccessModetoPVAccessMode(params.AccessMode)
	mode := k8sv1.PersistentVolumeFilesystem
	if params.VolumeBlockMode {
		mode = k8sv1.PersistentVolumeBlock
		//for block mode, we will default to ReadWriteMany to enable migration
		accessMode = []k8sv1.PersistentVolumeAccessMode{k8sv1.ReadWriteMany}
	}

	labels := map[string]string{DISKNAME: params.Name}

	return &cdiv1.DataVolume{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: params.NameSpace,
			Name:      strings.ToLower(params.Name),
			Labels:    labels,
		},
		Spec: cdiv1.DataVolumeSpec{
			Source: cdiv1.DataVolumeSource{
				HTTP: &cdiv1.DataVolumeSourceHTTP{
					URL: params.SfilePath,
				},
			},
			PVC: &k8sv1.PersistentVolumeClaimSpec{
				VolumeMode:  &mode,
				AccessModes: accessMode,
				Resources: k8sv1.ResourceRequirements{
					Requests: k8sv1.ResourceList{
						k8sv1.ResourceName(k8sv1.ResourceStorage): resource.MustParse(params.Capacity),
					},
				},
			},
		},
	}
}

// New DataVolume with Clone from another
func NewDataVolumeWithClone(params DiskParams) (*cdiv1.DataVolume, error) {
	var dv *cdiv1.DataVolume
	if params.CloneFromDisk != "" {
		dvList, _ := ListDVs(params.NameSpace)
		for _, d := range dvList {
			if d.Name == params.CloneFromDisk {
				dv = &d
				break
			}
		}
		if dv == nil || dv.Spec.PVC == nil {
			return nil, fmt.Errorf("Source disk %v to clone from could not be found", params.CloneFromDisk)
		}

		if dv.Spec.PVC.VolumeMode != nil {
			if *dv.Spec.PVC.VolumeMode == k8sv1.PersistentVolumeBlock && !params.VolumeBlockMode ||
				*dv.Spec.PVC.VolumeMode == k8sv1.PersistentVolumeFilesystem && params.VolumeBlockMode {
				return nil, fmt.Errorf("Cloning from a disk %s which is not same mode as disk %v is not supported", params.CloneFromDisk, params.Name)
			}
		}
		if dv.Status.Phase != cdiv1.Succeeded {
			return nil, fmt.Errorf("Source Disk %s is not in succeeded state. Cannot clone", params.CloneFromDisk)
		}
	}

	accessMode := VDiskAccessModetoPVAccessMode(params.AccessMode)
	labels := map[string]string{DISKNAME: params.Name}
	mode := k8sv1.PersistentVolumeFilesystem
	if params.VolumeBlockMode {
		mode = k8sv1.PersistentVolumeBlock
	}

	return &cdiv1.DataVolume{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: params.NameSpace,
			Name:      strings.ToLower(params.Name),
			Labels:    labels,
		},
		Spec: cdiv1.DataVolumeSpec{
			Source: cdiv1.DataVolumeSource{
				PVC: &cdiv1.DataVolumeSourcePVC{
					Namespace: params.NameSpace,
					Name:      params.CloneFromDisk,
				},
			},
			PVC: &k8sv1.PersistentVolumeClaimSpec{
				VolumeMode:  &mode,
				AccessModes: accessMode,
				Resources: k8sv1.ResourceRequirements{
					Requests: dv.Spec.PVC.Resources.Requests,
				},
			},
		},
	}, nil
}

// NewDataVolumeWithHTTPImport initializes a DataVolume struct with HTTP annotations
func NewDataVolumeEmptyDisk(params DiskParams) *cdiv1.DataVolume {
	accessMode := VDiskAccessModetoPVAccessMode(params.AccessMode)

	mode := k8sv1.PersistentVolumeFilesystem
	if params.VolumeBlockMode {
		mode = k8sv1.PersistentVolumeBlock
		//for block mode, we will default to ReadWriteMan to enable migration
		accessMode = []k8sv1.PersistentVolumeAccessMode{k8sv1.ReadWriteMany}
	}

	labels := map[string]string{DISKNAME: params.Name}

	return &cdiv1.DataVolume{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: params.NameSpace,
			Name:      strings.ToLower(params.Name),
			Labels:    labels,
		},
		Spec: cdiv1.DataVolumeSpec{
			Source: cdiv1.DataVolumeSource{
				Blank: &cdiv1.DataVolumeBlankImage{},
			},
			PVC: &k8sv1.PersistentVolumeClaimSpec{
				VolumeMode:  &mode,
				AccessModes: accessMode,
				Resources: k8sv1.ResourceRequirements{
					Requests: k8sv1.ResourceList{
						k8sv1.ResourceName(k8sv1.ResourceStorage): resource.MustParse(params.Capacity),
					},
				},
			},
		},
	}
}

type CloudConfigType string

const (
	CloudConfigTypeNoCloud     CloudConfigType = "NoCloudSource"
	CloudConfigTypeConfigDrive CloudConfigType = "ConfigDriveSource"
)

type CloudConfig struct {
	ConfigType           CloudConfigType
	UserDataSecretRef    string
	UserDataBase64       string
	UserData             string
	NetworkDataSecretRef string
	NetworkDataBase64    string
	NetworkData          string
}

func NewCloudInitConfig(c kubecli.KubevirtClient, vmName, ns string, config *CloudConfig) (*v1.CloudInitNoCloudSource, error) {
	usersecret := k8sv1.Secret{
		ObjectMeta: metav1.ObjectMeta{
		Name:      vmName+"-ciud",
		Namespace: ns,
	},
		Type: "Opaque",
	}

	nwsecret := k8sv1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      vmName+"-cind",
			Namespace: ns,
		},
		Type: "Opaque",
	}

	if config.UserDataBase64 != "" {
		uData, err := base64.StdEncoding.DecodeString(config.UserDataBase64)
		if err != nil {
			return nil, err
		}
		usersecret.Data = map[string][]byte{
			"userdata": uData,
		}
		usersecret.Labels = map[string]string{"dataType": "base64"}
	} else if config.UserData != "" {
		usersecret.Data = map[string][]byte{
			"userdata": []byte(config.UserData),
		}
		usersecret.Labels = map[string]string{"dataType": "string"}
	}

	if config.NetworkDataBase64 != "" {
		nwData, err := base64.StdEncoding.DecodeString(config.NetworkDataBase64)
		if err != nil {
			return nil, err
		}
		nwsecret.Data = map[string][]byte{
			"networkdata": nwData,
		}
		nwsecret.Labels = map[string]string{"dataType": "base64"}
	} else if config.NetworkData != "" {
		nwsecret.Data = map[string][]byte{
			"networkdata": []byte(config.NetworkData),
		}
		nwsecret.Labels = map[string]string{"dataType": "string"}
	}

	cloudInit := &v1.CloudInitNoCloudSource{}
	if config.UserData != "" || config.UserDataBase64 != "" {
		_, err := c.CoreV1().Secrets(ns).Create(&usersecret)
		if err != nil {
			if errors.IsAlreadyExists(err) {
				_, err = c.CoreV1().Secrets(ns).Update(&usersecret)
				if err != nil {
					return nil, err
				}
			} else {
				return nil, err
			}
		}
		cloudInit.UserDataSecretRef = &k8sv1.LocalObjectReference{Name: usersecret.Name}
	}

	if config.NetworkData != "" || config.NetworkDataBase64 != "" {
		_, err := c.CoreV1().Secrets(ns).Create(&nwsecret)
		if err != nil {
			if errors.IsAlreadyExists(err) {
				_, err = c.CoreV1().Secrets(ns).Update(&nwsecret)
				if err != nil {
					return nil, err
				}
			} else {
				return nil, err
			}
		}
		cloudInit.NetworkDataSecretRef = &k8sv1.LocalObjectReference{Name: nwsecret.Name}
	}

	return cloudInit, nil
}

func GetCloudInitUserData(c kubecli.KubevirtClient, ns, secret string) (string, string, error) {
	sec, err := c.CoreV1().Secrets(ns).Get(secret, metav1.GetOptions{})
	if err != nil {
		return "", "", err
	}
	labels := sec.ObjectMeta.GetLabels()
	if labels == nil {
		return "", "", fmt.Errorf("get CI Userdata invalid labels")
	}
	if dType, ok := labels["dataType"]; ok {
		if dType == "string" {
			if ud, k := sec.Data["userdata"]; k {
				return string(ud), "", nil
			}
			return "", "", fmt.Errorf("invalid userdata on secret")
		} else if dType == "base64"{
			if ud, k := sec.Data["userdata"]; k {
				ud64 := base64.StdEncoding.EncodeToString(ud)
				return "", ud64, nil
			}
			return "", "", fmt.Errorf("invalid userdata on secret")
		}
	}
	return "", "", fmt.Errorf("failed to find CI data type label")
}

func GetCloudInitNetworkData(c kubecli.KubevirtClient, ns, secret string) (string, string, error) {

	sec, err := c.CoreV1().Secrets(ns).Get(secret, metav1.GetOptions{})
	if err != nil {
		return "", "", err
	}
	labels := sec.ObjectMeta.GetLabels()
	if labels == nil {
		return "", "", fmt.Errorf("get CI NetworkData invalid labels")
	}
	if dType, ok := labels["dataType"]; ok {
		if dType == "string" {
			if nd, k := sec.Data["networkdata"]; k {
				return string(nd), "", nil
			}
			return "", "", fmt.Errorf("invalid userdata on secret")
		} else if dType == "base64"{
			if nd, k := sec.Data["networkdata"]; k {
				nd64 := base64.StdEncoding.EncodeToString(nd)
				return "", nd64, nil
			}
			return "", "", fmt.Errorf("invalid userdata on secret")
		}
	}
	return "", "", fmt.Errorf("failed to find CI data type label")
}

// Attach a CloudInit disk with the given cloud config
func AttachCloudInitDisk(c kubecli.KubevirtClient, vm *v1.VirtualMachine, config *CloudConfig) error {
	disk := v1.Disk{}
	disk.Name = CloudInitName
	disk.Disk = &v1.DiskTarget{Bus: "virtio"}
	vmSpec := &vm.Spec.Template.Spec
	vmSpec.Domain.Devices.Disks = append(vmSpec.Domain.Devices.Disks, disk)

	vol := v1.Volume{Name: CloudInitName}

	if config.ConfigType == CloudConfigTypeNoCloud {
		cloudInitNoCloud,err := NewCloudInitConfig(c, vm.Name, vm.Namespace, config)
		if err != nil {
			return err
		}
		vol.VolumeSource = v1.VolumeSource{CloudInitNoCloud: cloudInitNoCloud}
	} else {
		// FIXME - needs next version of kubevirt
	}
	vmSpec.Volumes = append(vmSpec.Volumes, vol)
	return nil
}

func AttachDisk(c kubecli.KubevirtClient, vm *v1.VirtualMachine, diskParams DiskParams) error {
	disk := v1.Disk{}
	disk.Name = strings.ToLower(diskParams.Name)
	disk.BootOrder = &diskParams.Order

	dt := "sata"
	if diskParams.Bus == "virtio" || diskParams.Bus == "scsi" || diskParams.Bus == "ide" {
		dt = diskParams.Bus
	}

	if diskParams.IsCdrom == true {
		readOnly := true
		disk.CDRom = &v1.CDRomTarget{Bus: dt, ReadOnly: &readOnly}
	} else {
		disk.Disk = &v1.DiskTarget{Bus: dt}
	}

	vmSpec := &vm.Spec.Template.Spec
	vmSpec.Domain.Devices.Disks = append(vmSpec.Domain.Devices.Disks, disk)

	vol := v1.Volume{}
	vol.Name = strings.ToLower(diskParams.Name)
	var pvcName string
	if diskParams.HxVolName == "" {
		pvcName = fmt.Sprintf("%s-%s", strings.ToLower(diskParams.Name), "pvc")
		if diskParams.VolumeHandle == "" {
			// data volume needs to be configured if Sfilepath exists
			if diskParams.SfilePath != "" {

				_, err := resource.ParseQuantity(diskParams.Capacity)
				if err != nil {
					return fmt.Errorf("Invalid disk Capacity: %v, error: %s", diskParams.Capacity, err.Error())
				}

				// check if its HTTP
				_, err = url.ParseRequestURI(diskParams.SfilePath)
				if err == nil {
					// HTTP source datavolume
					dv := NewDataVolumeWithHTTPImport(&diskParams)

					if diskParams.SourceCerts != "" {
						cert := NewCertConfigMap(&diskParams)
						err := CreateCertConfigMap(c, cert)
						if err != nil {
							return fmt.Errorf("failed to create cert configmap: %v", err)
						}
						dv.Spec.Source.HTTP.CertConfigMap = cert.Name
					} else {
						// lets look for default certs if available, we'll try to them
						_, err := c.CoreV1().ConfigMaps(DefaultNs).Get(DefaultTLSCert, metav1.GetOptions{})
						if err == nil {
							dv.Spec.Source.HTTP.CertConfigMap = DefaultTLSCert
						}
					}

					err = SetDVOwnerLabel(dv, vm.Name)
					if err != nil {
						return err
					}
					vm.Spec.DataVolumeTemplates = append(vm.Spec.DataVolumeTemplates, *dv)
					vol.DataVolume = &v1.DataVolumeSource{Name: dv.Name}
					vmSpec.Volumes = append(vmSpec.Volumes, vol)
				}
			} else if diskParams.CloneFromDisk != "" {
				dv, err := NewDataVolumeWithClone(diskParams)
				if err != nil || dv == nil {
					return err
				}
				err = SetDVOwnerLabel(dv, vm.Name)
				if err != nil {
					return err
				}
				vm.Spec.DataVolumeTemplates = append(vm.Spec.DataVolumeTemplates, *dv)
				vol.DataVolume = &v1.DataVolumeSource{Name: dv.Name}
				vmSpec.Volumes = append(vmSpec.Volumes, vol)
			} else if diskParams.DataDisk != "" {
				vol.DataVolume = &v1.DataVolumeSource{Name: diskParams.DataDisk}
				dv1, err := GetDV(diskParams.DataDisk, vm.Namespace)
				if err != nil {
					fmt.Printf("Data Disk DataVolume %v does not exist, %v", diskParams.DataDisk, err)
					return fmt.Errorf("Data Disk DataVolume %v does not exist, %v", diskParams.DataDisk, err)
				} else {
					err := SetDVOwnerLabel(dv1, vm.Name)
					if err != nil {
						return err
					}
					updateDataVolumeFromDefinition(dv1)
				}
				vmSpec.Volumes = append(vmSpec.Volumes, vol)
			} else {

				_, err := resource.ParseQuantity(diskParams.Capacity)
				if err != nil {
					return fmt.Errorf("Invalid disk Capacity: %v, error: %s", diskParams.Capacity, err.Error())
				}

				//empty disk
				dv := NewDataVolumeEmptyDisk(diskParams)
				err = SetDVOwnerLabel(dv, vm.Name)
				if err != nil {
					return err
				}
				vm.Spec.DataVolumeTemplates = append(vm.Spec.DataVolumeTemplates, *dv)
				vol.DataVolume = &v1.DataVolumeSource{Name: dv.Name}
				vmSpec.Volumes = append(vmSpec.Volumes, vol)
			}
		} else {
			// we have a VolumeHandle from user. This is a case where a disk is being created
			// with a volumeHandle that was saved from previous creation. We will
			// create a PV using that volumeHandle and tie the PVC to this PV
			pvc, err := GetPVC(c, pvcName)
			if err != nil || pvc == nil {
				pvName := fmt.Sprintf("%s-%s", diskParams.Name, "pv")
				_, err = CreateCSIPv(c, pvName, diskParams)
				fmt.Printf("Creating CSI PV for %s", diskParams.Name)
				if err != nil {
					fmt.Printf("Failed to create CSI PV for %s, err: %v", diskParams.Name, err)
					return err
				}
				fmt.Printf("\nCreating PVC for %s, class:%v", diskParams.Name, diskParams.Class)
				pvc, err = CreateISCSIPvc(c, pvcName, pvName, diskParams.Class, diskParams.Capacity, diskParams.NameSpace, false,
					diskParams.VolumeBlockMode, map[string]string{pvcCreatedByVM: "yes"})
				if err != nil {
					fmt.Printf("\nFailed to create PVC %s %v", pvcName, err)
					return err
				}
				updatePVCPrivate(c, pvc)
				vol.PersistentVolumeClaim = &k8sv1.PersistentVolumeClaimVolumeSource{
					ClaimName: pvcName,
				}
				vmSpec.Volumes = append(vmSpec.Volumes, vol)
			}
		}
	} else {
		pvcName = diskParams.HxVolName
		if ok, _ := IsHXVolumeAvailable(c, diskParams.HxVolName); !ok {
			fmt.Printf("HX Volume %s not found", pvcName)
			return fmt.Errorf("HX Volume %s not found", pvcName)
		}
		vol.PersistentVolumeClaim = &k8sv1.PersistentVolumeClaimVolumeSource{
			ClaimName: pvcName,
		}
		vmSpec.Volumes = append(vmSpec.Volumes, vol)
	}

	return nil
}

func GetVMIList(c kubecli.KubevirtClient, ns string) (*v1.VirtualMachineInstanceList, error) {
	vms, err := c.VirtualMachineInstance(ns).List(&metav1.ListOptions{})
	return vms, err
}

func GetVMList(c kubecli.KubevirtClient, ns string) (*v1.VirtualMachineList, error) {
	vms, err := c.VirtualMachine(ns).List(&metav1.ListOptions{})
	return vms, err
}

func GetVMIsOnNode(c kubecli.KubevirtClient, node, ns string) (*v1.VirtualMachineInstanceList, error) {
	selector := fmt.Sprintf("kubevirt.io/nodeName=%s", node)
	vms, err := c.VirtualMachineInstance(ns).List(&metav1.ListOptions{LabelSelector: selector})
	return vms, err
}

func GetPodList(c kubecli.KubevirtClient, ns string) (*k8sv1.PodList, error) {
	pods, err := c.CoreV1().Pods(ns).List(metav1.ListOptions{})
	return pods, err
}

func GetVirtPodList(c kubecli.KubevirtClient, nodeName, ns string) (*k8sv1.PodList, error) {
	lSelector := "kubevirt.io=virt-launcher"
	if nodeName != "" {
		fSelector := fmt.Sprintf("spec.nodeName=%s", nodeName)
		pods, err := c.CoreV1().Pods(ns).List(metav1.ListOptions{FieldSelector: fSelector, LabelSelector: lSelector})
		return pods, err
	} else  {
		pods, err := c.CoreV1().Pods(ns).List(metav1.ListOptions{LabelSelector: lSelector})
		return pods, err
	}
}

func GetVMIRSList(c kubecli.KubevirtClient, ns string) (*v1.VirtualMachineInstanceReplicaSetList, error) {
	vms, err := c.ReplicaSet(ns).List(metav1.ListOptions{})
	return vms, err
}

func GetVMIRS(c kubecli.KubevirtClient, vmname, ns string) (*v1.VirtualMachineInstanceReplicaSet, error) {
	list, err := GetVMIRSList(c, ns)
	if err != nil {
		return nil, err
	}
	for _, vmrs := range list.Items {
		if vmrs.Spec.Template.ObjectMeta.Name == vmname {
			return &vmrs, nil
		}
	}

	return nil, fmt.Errorf("VMI Replicaset %s not found", vmname)
}

func GetVMI(c kubecli.KubevirtClient, vmname, ns string) (*v1.VirtualMachineInstance, error) {
	list, err := GetVMIList(c, ns)
	if err != nil {
		return nil, err
	}
	for _, vmi := range list.Items {
		for _, ref := range vmi.OwnerReferences {
			if ref.Name == vmname {
				return &vmi, nil
			}
		}
	}

	errStr := fmt.Sprintf("VMI %s not found", vmname)
	return nil, errors2.New(errStr)
}

func GetVM(c kubecli.KubevirtClient, vmname, ns string) (*v1.VirtualMachine, error) {
	return c.VirtualMachine(ns).Get(vmname, &metav1.GetOptions{})
}

func GetVMPodRef(c kubecli.KubevirtClient, vmName, ns string) (string, error) {
	label := map[string]string{"name": vmName}

	//filter our request to find pods for this VM
	list, err := c.CoreV1().Pods(ns).List(metav1.ListOptions{LabelSelector: labels.Set(label).String()})
	if err != nil {
		return "", err
	}

	vmref := ""
	var ts *time.Time
	for _, pod := range list.Items {
		// look for the oldest running pod. That is the pod that the VM is on
		// if a migration is in progress, there will be a newer pod and VM
		// will not be running on it yet.
		if pod.Status.Phase == "Running" {
			if ts == nil {
				t := pod.GetCreationTimestamp().Time
				ts = &t
				vmref = strings.TrimPrefix(pod.Name, "virt-launcher-")
			} else {
				// is there an older pod
				if pod.GetCreationTimestamp().Time.Sub(*ts) < 0 {
					t := pod.GetCreationTimestamp().Time
					ts = &t
					vmref = strings.TrimPrefix(pod.Name, "virt-launcher-")
				}
			}
		}
	}
	if vmref != "" {
		return vmref, nil
	}
	return "", fmt.Errorf("Pod for VM %s not found", vmName)
}

// GetCdiClient gets an instance of a kubernetes client that includes all the CDI extensions.
func GetCdiClient() (*cdiClientset.Clientset, error) {
	cfg, err := clientcmd.BuildConfigFromFlags("", "/opt/cisco/cluster-config")
	if err != nil {
		return nil, err
	}
	cdiClient, err := cdiClientset.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return cdiClient, nil
}

func GetDV(dvName, ns string) (*cdiv1.DataVolume, error) {
	c, err := GetCdiClient()
	if err != nil {
		return nil, err
	}
	return c.CdiV1alpha1().DataVolumes(ns).Get(dvName, metav1.GetOptions{})
}

func IsDownloadInProgress(vm *v1.VirtualMachine, Ns string) bool {
	for _, dv := range vm.Spec.DataVolumeTemplates {
		dv, err := GetDV(dv.Name, Ns)
		if err != nil && dv.Status.Phase != cdiv1.Succeeded &&
			dv.Status.Phase != cdiv1.Failed && dv.Status.Phase != cdiv1.Unknown {
			return true
		}
	}
	return false
}

func DeleteVM(c kubecli.KubevirtClient, name, ns string) error {
	return c.VirtualMachine(ns).Delete(name, &metav1.DeleteOptions{})
}

func DeleteVMIRS(c kubecli.KubevirtClient, name, ns string) error {
	return c.ReplicaSet(ns).Delete(name, &metav1.DeleteOptions{})
}

func DeletePVC(c kubecli.KubevirtClient, name, ns string) error {
	return c.CoreV1().PersistentVolumeClaims(ns).Delete(name, &metav1.DeleteOptions{})
}

func GetPv(c kubecli.KubevirtClient, name string) (*k8sv1.PersistentVolume, error) {
	pv, err := c.CoreV1().PersistentVolumes().Get(name, metav1.GetOptions{})
	if err != nil || pv == nil {
		return nil, err
	}
	return pv, err
}

func GetDefaultStorageProvisioner(c kubecli.KubevirtClient) (string, error) {
	options := metav1.ListOptions{}

	scList, err := c.StorageV1().StorageClasses().List(options)
	if err != nil {
		return "", err
	}

	for _, sc := range scList.Items {
		ann := sc.GetObjectMeta().GetAnnotations()
		if ann["storageclass.kubernetes.io/is-default-class"] == "true" ||
		   ann["storageclass.beta.kubernetes.io/is-default-class"] == "true" {
			return sc.Provisioner, nil
		}
	}

	return "", err
}

func GetPvDetails(c kubecli.KubevirtClient, name string) (string, int32, int64, error) {
	pv, err := c.CoreV1().PersistentVolumes().Get(name, metav1.GetOptions{})
	if err != nil || pv == nil {
		return "", 0, 0, err
	}
	mode := k8sv1.PersistentVolumeFilesystem
	if pv.Spec.VolumeMode != nil {
		mode = *pv.Spec.VolumeMode
	}
	rs := pv.Spec.Capacity["storage"]
	storage, _ := (&rs).AsInt64()
	if pv.Spec.ISCSI == nil {
		return string(mode), 0, storage, nil
	}

	return string(mode), pv.Spec.ISCSI.Lun, storage, nil
}

// Return PVC reference
func GetPVC(c kubecli.KubevirtClient, name string) (*k8sv1.PersistentVolumeClaim, error) {
	pvc, err := c.CoreV1().PersistentVolumeClaims("default").Get(name, metav1.GetOptions{})
	if err != nil || pvc == nil {
		return nil, err
	}
	return pvc, err
}

// Assign affinity and anti affinity labels
func AddAppAffinityLabels(vmi *v1.VirtualMachine, affinityLabel, antiAffinityLabel [][]string) error {

	affLables := k8sv1.Affinity{}

	if affinityLabel != nil && len(affinityLabel) != 0 {
		podAff := k8sv1.PodAffinity{}
		for _, label := range affinityLabel {
			if len(label) < 3 {
				return fmt.Errorf("invalid Affinity labels :%v", label)
			}
			if label[2] == "required" {
				podTerm := getAffinityTerm(label[0], label[1])
				podAff.RequiredDuringSchedulingIgnoredDuringExecution =
					append(podAff.RequiredDuringSchedulingIgnoredDuringExecution, podTerm)
			} else {
				podTerm := getWeightedAffinityTerm(label[0], label[1])
				podAff.PreferredDuringSchedulingIgnoredDuringExecution =
					append(podAff.PreferredDuringSchedulingIgnoredDuringExecution, podTerm)
			}
		}
		affLables.PodAffinity = &podAff
	}

	if antiAffinityLabel != nil && len(antiAffinityLabel) != 0 {
		podAntiAff := k8sv1.PodAntiAffinity{}
		for _, label := range antiAffinityLabel {
			if len(label) < 3 {
				return fmt.Errorf("invalid AntiAffinity labels :%v", label)
			}
			if label[2] == "required" {
				podTerm := getAffinityTerm(label[0], label[1])
				podAntiAff.RequiredDuringSchedulingIgnoredDuringExecution =
					append(podAntiAff.RequiredDuringSchedulingIgnoredDuringExecution, podTerm)
			} else {
				podTerm := getWeightedAffinityTerm(label[0], label[1])
				podAntiAff.PreferredDuringSchedulingIgnoredDuringExecution =
					append(podAntiAff.PreferredDuringSchedulingIgnoredDuringExecution, podTerm)
			}
		}
		affLables.PodAntiAffinity = &podAntiAff
	}

	vmi.Spec.Template.Spec.Affinity = &affLables

	return nil
}

func ListNodes(client kubecli.KubevirtClient) (*k8sv1.NodeList, error) {
	return client.CoreV1().Nodes().List(metav1.ListOptions{})
}

// Get Node Info
func GetNode(client kubecli.KubevirtClient, nodeName string) (*k8sv1.Node, error) {
	return client.CoreV1().Nodes().Get(nodeName, metav1.GetOptions{})
}

func getAffinityTerm(key, val string) k8sv1.PodAffinityTerm {

	labelSelReq := metav1.LabelSelectorRequirement{Key: key, Operator: metav1.LabelSelectorOpIn,
		Values: []string{val}}
	labelSel := metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{labelSelReq}}
	podTerm := k8sv1.PodAffinityTerm{LabelSelector: &labelSel, TopologyKey: "kubernetes.io/hostname"}

	return podTerm
}

func getWeightedAffinityTerm(key, val string) k8sv1.WeightedPodAffinityTerm {

	labelSelReq := metav1.LabelSelectorRequirement{Key: key, Operator: metav1.LabelSelectorOpIn,
		Values: []string{val}}
	labelSel := metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{labelSelReq}}
	podTerm := k8sv1.PodAffinityTerm{LabelSelector: &labelSel, TopologyKey: "kubernetes.io/hostname"}
	wPodTerm := k8sv1.WeightedPodAffinityTerm{Weight: 1, PodAffinityTerm: podTerm}
	return wPodTerm
}

// drain pods from node and cardon it
func CordonNode(client kubecli.KubevirtClient, nodeName string, nodeOffline bool) error {

	nodeInfo, err := GetNode(client, nodeName)
	if err != nil {
		return err
	}

	nodeInfo.Spec.Unschedulable = nodeOffline
	// put node
	_, err = client.CoreV1().Nodes().Update(nodeInfo)
	if err != nil {
		return err
	}

	return nil
}

func GetClusterUUID(c kubecli.KubevirtClient) string {

	nspace, err := c.CoreV1().Namespaces().Get("kube-system", metav1.GetOptions{})
	if err == nil {
		return string(nspace.UID)
	} else {
		return ""
	}
}

type ResourceEvent struct {
	ObjName   string
	ObjType   string
	ObjUUID   string
	Reason    string
	Msg       string
	Component string
	Type      string
	UID       string
	Host      string
	FirstSeen time.Time
	LastSeen  time.Time
}

type EventFilter struct {
	kbMsgHint string
	transMsg  string
}

// Most of the event messaging generated from Kubernetes/Kubevirt will not make
// sense for a user. The below table helps in filtering those out and
// or translate them to better versions of the message
var filterDb = [...]EventFilter{
	{"Created virtual machine pod ", "Virtual machine start has been initiated"},
	{"Signaled Graceful Shutdown", "Graceful shutdown of Virtual Machine initiated"},
	{"Deleted finished virtual machine", "Virtual machine has been stopped"},
	{"VirtualMachineInstance started", "Virtual Machine started"},
	{"The VirtualMachineInstance was shut down", "Virtual Machine has been stopped"},
	{"Failed to open", ""},
	{"Failed to copy", ""},
	{"Unable to mount volumes", ""},
	{"The VirtualMachineInstance crashed", "Virtual Machine has been stopped"},
	{"initialization failed for volume", ""},
	{"NodeNotSchedulable", "node is in maintenance mode"},
	{"NodeSchedulable", "node is in active state"},
	{"NodeReady", "node is now active"},
	{"NodeNotReady", "node is now inactive"},
	{"NodeHasSufficientMemory", "node has enough available resources."},
	{"NodeHasInSufficientMemory", "node has insufficient memory."},
	{"status is now: MemoryPressure", "node does not have enough memory available."},
	{"NodeHasDiskPressure", "node does not have enough space on disk."},
	{"NodeHasNoDiskPressure", "node has enough available resources."},
	{"NodeHasSufficientPID", "node has enough available resources."},
	{"NodeHasInsufficientPID", "node does not have enough process IDs available"},
	{"has been rebooted", "node has been rebooted"},
	{"Registered Node", "Node added successfully to the compute cluster."},
	{"Starting kubelet", "Started node initialization."},
	{"System OOM encountered", "System is experiencing out of memory condition"},
	{"Import into", ""},
	{"Created DataVolume", ""},
	{"Deleted DataVolume", ""},
	{"Failed to import", "Failed to import image"},
	{"Unable to connect", ""},
	{"Successfully imported", ""},
	{"Successfully cloned ", ""},
	{"Cloning from ", ""},
	{"Created migration target", "Migration of VM initiated"},
	{"VirtualMachineInstance is migrating", "VM is migrating"},
	{"node reported migration succeeded", "VM successfully migrated"},
}

func filterOutMsg(msg string) (bool, *EventFilter) {
	for _, m := range filterDb {
		if strings.Contains(msg, m.kbMsgHint) {
			return false, &m
		}
	}
	return true, &EventFilter{}
}

func sendOlderEvents(rC chan ResourceEvent, oldEventList *k8sv1.EventList) {
	// send all older events
	for _, e := range oldEventList.Items {
		if filter, newevent := filterOutMsg(e.Message); !filter {
			msg := e.Message
			if newevent.transMsg != "" {
				msg = newevent.transMsg
			}
			rEvent := ResourceEvent{
				e.InvolvedObject.Name,
				"",
				"",
				e.Reason,
				msg,
				e.Source.Component,
				e.Type,
				string(e.UID),
				e.Source.Host,
				e.FirstTimestamp.Time,
				e.LastTimestamp.Time,
			}
			rC <- rEvent
		}
	}
}

func WatchKbEvents(client kubecli.KubevirtClient, rC chan ResourceEvent, quitDone chan bool, kbQuit chan bool) error {
	api := client.CoreV1()
	listOpts := metav1.ListOptions{
		//LabelSelector: "kubevirt.io=virt-launcher",
	}
	events, err := api.Events("").List(listOpts)
	if err != nil {
		return err
	}
	timeout := int64(0)
	eventsWatcher, err := api.Events("").Watch(
		metav1.ListOptions{
			Watch:           true,
			ResourceVersion: events.ResourceVersion,
			TimeoutSeconds:  &timeout})
	if err != nil {
		return err
	}

	oldEventList, err := api.Events("").List(listOpts)
	if err != nil {
		fmt.Printf("Error getting previous older Events: %v", err)
	}
	fmt.Printf("Starting to monitor events\n")
	lastmessage := ""
	go func() {
		sendOlderEvents(rC, oldEventList)
		watcherChan := eventsWatcher.ResultChan()
		for {
			select {
			case <-kbQuit:
				fmt.Printf("\nQuitting Kb event watch service")
				eventsWatcher.Stop()
				quitDone <- true
				return
			case event, ok := <-watcherChan:
				if !ok {
					eventsWatcher.Stop()
					fmt.Printf("\nKB Channel closed\n")
					quitDone <- false
					return
				}
				e, isEvent := event.Object.(*k8sv1.Event)
				if !isEvent {
					continue
				}
				if filter, newevent := filterOutMsg(e.Message); !filter {
					msg := e.Message
					if newevent.transMsg != "" {
						msg = newevent.transMsg
					}
					rEvent := ResourceEvent{
						e.InvolvedObject.Name,
						"",
						"",
						e.Reason,
						msg,
						e.Source.Component,
						e.Type,
						string(e.UID),
						e.Source.Host,
						e.FirstTimestamp.Time,
						e.LastTimestamp.Time,
					}
					rC <- rEvent
				} else {
					if !strings.Contains(e.Message, lastmessage) {
						fmt.Printf("\nraw: %v", e)
						lastmessage = e.Message
					}
				}
			}
		}
	}()

	return nil
}

// VolumePVC read only information
func IsPVCReadOnly(client kubecli.KubevirtClient, claim k8sv1.PersistentVolumeClaim) bool {

	for _, a := range claim.Status.AccessModes {
		if a == k8sv1.ReadOnlyMany {
			return true
		}
	}

	return false
}

// check if hx volume is present
func IsHXVolumeAvailable(client kubecli.KubevirtClient, volName string) (bool, error) {

	vList, err := ListPVCs(client)
	if err != nil {
		return false, err
	}
	for _, v := range vList.Items {
		if (*v.Spec.StorageClassName == "csi-hxcsi") && (v.Name == volName) {
			return true, nil
		}
	}
	return false, nil
}

// is PVC created during VM creation
func IsPVCGenVM(claim k8sv1.PersistentVolumeClaim) bool {

	for k, _ := range claim.GetObjectMeta().GetAnnotations() {
		if k == pvcCreatedByVM || k == pvcCDICreated {
			return true
		}
	}

	return false
}

// check if file exists
func fileExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

const (
	dataVolumePollInterval = 3 * time.Second
	dataVolumeCreateTime   = 60 * time.Second

	DISKOWNER_LABEL = "DiskOwner"
	DISKTYPE_LABEL  = "DiskType"
	DISKTYPE_DATA   = "DataDisk"
	DISKNAME        = "DiskName"
)

// Obtain the ref to the kubevirt client object
func GetCDIClient() (*cdiClientset.Clientset, error) {
	home := os.Getenv("HOME")
	if home == "" {
		home = os.Getenv("USERPROFILE")
	}

	// use kubeconfig from fixed location
	kubeconfig := "/opt/cisco/cluster-config"

	if exists, _ := fileExists(kubeconfig); !exists {
		kubeconfig = filepath.Join(home, ".kube", "config")
	}

	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, err
	}
	cdiClient, err := cdiClientset.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}

	return cdiClient, nil
}

// Create a Datavolume from a given definition.
func createDataVolumeFromDefinition(clientSet *cdiClientset.Clientset, namespace string, def *cdiv1.DataVolume) (*cdiv1.DataVolume, error) {
	var dataVolume *cdiv1.DataVolume
	err := wait.PollImmediate(dataVolumePollInterval, dataVolumeCreateTime, func() (bool, error) {
		var err error
		dataVolume, err = clientSet.CdiV1alpha1().DataVolumes(namespace).Create(def)
		if err == nil || apierrs.IsAlreadyExists(err) {
			return true, nil
		}
		return false, err
	})
	if err != nil {
		return nil, err
	}
	return dataVolume, nil
}

// Update a given datavolume
func updateDataVolumeFromDefinition(def *cdiv1.DataVolume) (*cdiv1.DataVolume, error) {
	c, err := GetCDIClient()
	if err != nil {
		return nil, err
	}

	ns := def.Namespace
	var dataVolume *cdiv1.DataVolume
	err = wait.PollImmediate(dataVolumePollInterval, dataVolumeCreateTime, func() (bool, error) {
		var err error
		dataVolume, err = c.CdiV1alpha1().DataVolumes(ns).Update(def)
		if err == nil || apierrs.IsAlreadyExists(err) {
			return true, nil
		}
		return false, err
	})
	if err != nil {
		return nil, err
	}
	return dataVolume, nil
}

func SetDVLabels(dv *cdiv1.DataVolume, label, value string) {
	labels := dv.ObjectMeta.GetObjectMeta().GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	labels[label] = value
	dv.ObjectMeta.GetObjectMeta().SetLabels(labels)
}

func SetDVOwnerLabel(dv *cdiv1.DataVolume, owner string) error {
	labels := dv.ObjectMeta.GetObjectMeta().GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	if v, ok := labels[DISKOWNER_LABEL]; ok {
		if v != owner {
			return fmt.Errorf("cannot attach disk %v to vm:%v as it is already in use by %v", dv.Name, owner, v)
		}
		fmt.Printf("dv:%v, current:%v, new:%v", dv.Name, owner, v)
	}
	labels[DISKOWNER_LABEL] = owner
	dv.ObjectMeta.GetObjectMeta().SetLabels(labels)
	return nil
}

func isDVDataDisk(dv *cdiv1.DataVolume) bool {
	labels := dv.ObjectMeta.GetObjectMeta().GetLabels()
	if labels == nil {
		return false
	}
	if dType, ok := labels[DISKTYPE_LABEL]; ok {
		if dType == DISKTYPE_DATA {
			return true
		}
	}
	return false
}

func IsDVDataDisk(dvName, ns string) bool {
	dv, err := GetDV(dvName, ns)
	if err != nil {
		return false
	}
	labels := dv.ObjectMeta.GetObjectMeta().GetLabels()
	if labels == nil {
		return false
	}
	if dType, ok := labels[DISKTYPE_LABEL]; ok {
		if dType == DISKTYPE_DATA {
			return true
		}
	}
	return false
}

// Remove Owner Labels on the DV
func RemoveDVOwnerLabel(dvName, owner, ns string) error {
	dv, err := GetDV(dvName, ns)
	if err != nil {
		return err
	}
	if !isDVDataDisk(dv) {
		fmt.Printf("Not a data disk %v, cannot remove labels", dv.Name)
		return fmt.Errorf("not a data disk %v, cannot remove labels", dv.Name)
	}
	labels := dv.ObjectMeta.GetObjectMeta().GetLabels()
	if labels == nil {
		fmt.Printf("Failed to find dv:%v labels", dv.Name)
		return fmt.Errorf("failed to find dv:%v labels", dv.Name)
	}
	if val, ok := labels[DISKOWNER_LABEL]; ok {
		if val == owner {
			delete(labels, DISKOWNER_LABEL)
			dv.ObjectMeta.GetObjectMeta().SetLabels(labels)
			updateDataVolumeFromDefinition(dv)
		}
	}
	fmt.Printf("Failed to find matching label on dv:%v user:%v", dv.Name, owner)
	return fmt.Errorf("failed to find matching label on dv:%v user:%v", dv.Name, owner)
}

// Create the DataVolume for the vdisk
func CreateDataVolume(params DiskParams, kc kubecli.KubevirtClient, ns string) error {
	var dv *cdiv1.DataVolume
	var err error

	_, err = resource.ParseQuantity(params.Capacity)
	if err != nil {
		return fmt.Errorf("Invalid disk Capacity: %s, %s", params.Capacity, err.Error())
	}

	if params.SfilePath != "" {
		// check if its HTTP
		_, err = url.ParseRequestURI(params.SfilePath)
		if err == nil {
			dv = NewDataVolumeWithHTTPImport(&params)

			if params.SourceCerts != "" {
				cert := NewCertConfigMap(&params)
				err := CreateCertConfigMap(kc, cert)
				if err != nil {
					return fmt.Errorf("failed to create cert configmap: %v", err)
				}
				dv.Spec.Source.HTTP.CertConfigMap = cert.Name
			} else {
				// lets look for default certs if available, we'll try to the
				_, err := kc.CoreV1().ConfigMaps(DefaultNs).Get(DefaultTLSCert, metav1.GetOptions{})
				if err == nil {
					dv.Spec.Source.HTTP.CertConfigMap = DefaultTLSCert
				}
			}
		} else {
			return err
		}
	} else if params.CloneFromDisk != "" {
		dv, err = NewDataVolumeWithClone(params)
		if err != nil || dv == nil {
			return err
		}
	} else {
		dv = NewDataVolumeEmptyDisk(params)
	}
	if dv == nil {
		return fmt.Errorf("Failed to create disk %v", params.Name)
	}

	SetDVLabels(dv, DISKTYPE_LABEL, DISKTYPE_DATA)
	SetDVLabels(dv, DISKNAME, params.Name)

	c, err := GetCDIClient()
	if err != nil {
		return err
	}

	_, err = createDataVolumeFromDefinition(c, ns, dv)

	return err
}

// List all the DVs in the given namespace
func ListDVs(ns string) ([]cdiv1.DataVolume, error) {
	c, err := GetCDIClient()
	if err != nil {
		return nil, err
	}
	dvList, err := c.CdiV1alpha1().DataVolumes(ns).List(metav1.ListOptions{})

	return dvList.Items, err
}

// Return the disk with right dv Name
func GetDiskDV(name, ns string) (*cdiv1.DataVolume, error) {
	c, err := GetCDIClient()
	if err != nil {
		return nil, err
	}

	dv, err := c.CdiV1alpha1().DataVolumes(ns).Get(name, metav1.GetOptions{})
	if err != nil || dv == nil {
		return nil, fmt.Errorf("Could not find disk %v, err:%v", name, err)
	}

	labels := dv.ObjectMeta.GetObjectMeta().GetLabels()
	if labels != nil {
		if name, ok := labels[DISKNAME]; ok {
			dv.Name = name
		}
	}

	return dv, nil
}

// Delete a DataDisk
func DeleteDataDisk(kv kubecli.KubevirtClient, diskName, ns string) error {
	c, err := GetCDIClient()
	if err != nil {
		return err
	}

	diskName = strings.ToLower(diskName)

	dv, err := c.CdiV1alpha1().DataVolumes(ns).Get(diskName, metav1.GetOptions{})
	if err != nil || dv == nil {
		return fmt.Errorf("Could not find disk %v, err:%v", diskName, err)
	}

	labels := dv.ObjectMeta.GetObjectMeta().GetLabels()
	if labels != nil {
		if owner, ok := labels[DISKOWNER_LABEL]; ok {
			return fmt.Errorf("disk %v is in use by VM %v and cannot be deleted", diskName, owner)
		}
	}

	if dv.Spec.Source.HTTP != nil && dv.Spec.Source.HTTP.CertConfigMap != "" && dv.Spec.Source.HTTP.CertConfigMap != DefaultTLSCert {
		kv.CoreV1().ConfigMaps(ns).Delete(dv.Spec.Source.HTTP.CertConfigMap, &metav1.DeleteOptions{})
	}
	return c.CdiV1alpha1().DataVolumes(ns).Delete(diskName, &metav1.DeleteOptions{})
}

// Trigger live migration on the given VM
func LiveMigrateVM(c kubecli.KubevirtClient, vmName, ns string) error {
	if c == nil {
		return fmt.Errorf("Invalid kubevirt client")
	}

	migrate := &v1.VirtualMachineInstanceMigration{
		ObjectMeta: metav1.ObjectMeta{
			Name:      vmName + "-migration",
			Namespace: ns,
		},
		Spec: v1.VirtualMachineInstanceMigrationSpec{VMIName: vmName},
	}

	jobs, err := c.VirtualMachineInstanceMigration(ns).List(&metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, m := range jobs.Items {
		if m.Spec.VMIName == vmName {
			if m.Status.Phase != v1.MigrationSucceeded &&
				m.Status.Phase != v1.MigrationFailed {
				return fmt.Errorf("A migration job is already in progress")
			}
		}
	}
	c.VirtualMachineInstanceMigration(ns).Delete(migrate.Name, &metav1.DeleteOptions{})
	_, err = c.VirtualMachineInstanceMigration(ns).Create(migrate)
	return err
}

// Cancel live migration on the given VM
func CancelLiveMigrateVM(c kubecli.KubevirtClient, vmName, ns string) error {
	if c == nil {
		return fmt.Errorf("Invalid kubevirt client")
	}

	migrate := &v1.VirtualMachineInstanceMigration{
		ObjectMeta: metav1.ObjectMeta{
			Name:      vmName + "-migration",
			Namespace: ns,
		},
		Spec: v1.VirtualMachineInstanceMigrationSpec{VMIName: vmName},
	}

	jobs, err := c.VirtualMachineInstanceMigration(ns).List(&metav1.ListOptions{})
	if err != nil {
		return err
	}
	if len(jobs.Items) == 0 {
		return fmt.Errorf("No migration job in progress to cancel")
	}

	c.VirtualMachineInstanceMigration(ns).Delete(migrate.Name, &metav1.DeleteOptions{})
	return err
}

// Get live migration status
func GetLiveMigrateStatus(c kubecli.KubevirtClient, vmName, ns string) (string, error) {
	if c == nil {
		return "", fmt.Errorf("Invalid kubevirt client")
	}

	jobs, err := c.VirtualMachineInstanceMigration(ns).List(&metav1.ListOptions{})
	if err != nil {
		return "", err
	}

	for _, m := range jobs.Items {
		if m.Spec.VMIName == vmName {
			if m.Status.Phase == v1.MigrationRunning {
				return "Migration in Progress", nil
			}
			return string(m.Status.Phase), nil
		}
	}
	return "", nil
}

// Find all completed virt-launcher pods and delete them. We don't want them to hang around
func CleanupCompletedVMPODs(c kubecli.KubevirtClient, ns string) (error, []string) {
	fSelector := map[string]string{"status.phase": "Succeeded"}
	lSelector := map[string]string{"kubevirt.io": "virt-launcher"}
	var vms []string

	// the old live migrated VM will leave behind a virt-launcher POD that
	// is in succeeded state. Filter our request to find those and then clean them up
	list, err := c.CoreV1().Pods(ns).List(metav1.ListOptions{FieldSelector: labels.Set(fSelector).String(), LabelSelector: labels.Set(lSelector).String()})
	if err != nil {
		return err, nil
	}

	for _, pod := range list.Items {
		//make sure only the completed VM virt-launcher pods are cleaned up
		if strings.Contains(pod.Name, "virt-launcher") {
			c.CoreV1().Pods(ns).Delete(pod.Name, &metav1.DeleteOptions{})
			vms = append(vms, pod.Name)
		}
	}
	return nil, vms
}

func GetVMNetwork(c kubecli.KubevirtClient, name, ns string) (*networkv1.NetworkAttachmentDefinition, error) {
	return c.NetworkClient().K8sCniCncfIoV1().NetworkAttachmentDefinitions(ns).Get(name, metav1.GetOptions{})
}

// setup Kubevirt unschedulable taint
func SetNodeKVUnSchedulable(c kubecli.KubevirtClient, nodeName string, set bool) error {
	node, err := c.CoreV1().Nodes().Get(nodeName, metav1.GetOptions{})
	if err != nil {
		return err
	}

	old, err := json.Marshal(node)
	if err != nil {
		return err
	}
	new := node.DeepCopy()

	if set {
		new.Spec.Taints = append(new.Spec.Taints, k8sv1.Taint{
			Key:    "kubevirt.io/drain",
			Effect: k8sv1.TaintEffectNoSchedule,
		})
	} else {
		// remove the taint.
		oldTaints := new.Spec.Taints
		new.Spec.Taints = []k8sv1.Taint{}
		for _, taint := range oldTaints {
			if taint.Key != "kubevirt.io/drain" {
				new.Spec.Taints = append(new.Spec.Taints, taint)
			}
		}
	}

	newJson, err := json.Marshal(new)
	if err != nil {
		return err
	}
	patch, err := strategicpatch.CreateTwoWayMergePatch(old, newJson, node)
	if err != nil {
		return err
	}
	_, err = c.CoreV1().Nodes().Patch(node.Name, types.StrategicMergePatchType, patch)
	if err != nil {
		return err
	}
	return nil
}

// Update a given VM
func updateVMFromDefinition(c kubecli.KubevirtClient, def *v1.VirtualMachine) (error) {
	err := wait.PollImmediate(dataVolumePollInterval, dataVolumeCreateTime, func() (bool, error) {
		var err error
		_, err = c.VirtualMachine(def.Namespace).Update(def)
		if err == nil || apierrs.IsAlreadyExists(err) {
			return true, nil
		}
		return false, err
	})
	if err != nil {
		return err
	}
	return nil
}

func setVMLabel(c kubecli.KubevirtClient, vm *v1.VirtualMachine, label, value string) error {
	labels := vm.ObjectMeta.GetObjectMeta().GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	labels[label] = value
	vm.ObjectMeta.GetObjectMeta().SetLabels(labels)
	return updateVMFromDefinition(c, vm)
}

// Remove VM label
func removeVMLabel(c kubecli.KubevirtClient, vm *v1.VirtualMachine, label string) error {
	labels := vm.ObjectMeta.GetObjectMeta().GetLabels()
	if labels == nil {
		fmt.Printf("Failed to find vm:%v labels", vm.Name)
		return fmt.Errorf("failed to find vm:%v labels", vm.Name)
	}
	if _, ok := labels[label]; ok {
			delete(labels, label)
			vm.ObjectMeta.GetObjectMeta().SetLabels(labels)
			return updateVMFromDefinition(c, vm)
	}
	return nil
}

func IsVMMarkedForDelete(vm *v1.VirtualMachine) bool {
	labels := vm.ObjectMeta.GetObjectMeta().GetLabels()
	if labels == nil {
		return false
	}
	if _, ok := labels["AP_DELETE_VM"]; ok {
		return true
	}
	return false
}

func VMAddDeleteLabel(c kubecli.KubevirtClient, vm *v1.VirtualMachine) error {
	return setVMLabel(c, vm, "AP_DELETE_VM", "true")
}

func VMRemoveDeleteLabel(c kubecli.KubevirtClient, vm *v1.VirtualMachine) error {
	return removeVMLabel(c, vm, "AP_DELETE_VM")
}

func AbortPendingEvictionMigrations(c kubecli.KubevirtClient, nodeName string) error {
	migs, err := c.VirtualMachineInstanceMigration(DefaultNs).List(&metav1.ListOptions{})
	if err != nil {
		return err
	}

	// Get all VMIs on node  that is aborting eviction
	vmis, err := GetVMIsOnNode(c, nodeName, DefaultNs)
	if err !=  nil {
		return err
	}
	vmiMap := make(map[string]v1.VirtualMachineInstance)
	for _, vmi := range vmis.Items {
		vmiMap[vmi.Name] = vmi
	}

	var er error
	for _, mig := range migs.Items {
		_, existsOnNode := vmiMap[mig.Spec.VMIName]
		// is not running or done, clean it up
		if (mig.Status.Phase == v1.MigrationScheduling ||
			mig.Status.Phase == v1.MigrationScheduled ||
			mig.Status.Phase == v1.MigrationPending ||
			mig.Status.Phase == v1.MigrationFailed ||
			mig.Status.Phase == v1.MigrationSucceeded ||
			mig.Status.Phase == v1.MigrationPhaseUnset) && existsOnNode {
			mig.Finalizers = make([]string, 0)
			fmt.Printf("Cleaning up migration: %v for VM: %v", mig.Name, mig.Spec.VMIName)
			_, err := c.VirtualMachineInstanceMigration(DefaultNs).Update(&mig)
			if err != nil {
				er = err
				fmt.Printf("Failed to update migration object: %v, VMI:%v, err:%v", mig.Name, mig.Spec.VMIName, err)
			}
			err = c.VirtualMachineInstanceMigration(DefaultNs).Delete(mig.Name, &metav1.DeleteOptions{})
			if err != nil {
				er = err
				fmt.Printf("Failed to clean up migration object: %v, VMI:%v, err:%v", mig.Name, mig.Spec.VMIName, err)
			} else {
				fmt.Printf("Cleaned up eviction migration for VMI: %v/%v\n", mig.Spec.VMIName, mig.Name)
			}
		}
	}

	podList, err := GetVirtPodList(c, nodeName, DefaultNs)
	if err != nil {
		return err
	}

	// Cleanup any pods that are left behind. If VM cannot be
	// migrated, virt-launch pods are left in pending state. clean
	// them up as we are aborting.
	for _, pod := range podList.Items {
		if jobName, ok := pod.Annotations["kubevirt.io/migrationJobName"]; ok {
			if strings.Contains(jobName, "kubevirt-evacuation") {
				err = c.CoreV1().Pods(DefaultNs).Delete(pod.Name, &metav1.DeleteOptions{})
				if err != nil {
					fmt.Printf("Failed to clean up eviction migration pod: %v, err: %v", pod.Name, err)
				} else {
					fmt.Printf("Eviction migration POD:%v cleanup\n", pod.Name)
				}
			}
		}
	}

	return er
}

func GetEvacuationMigrationObjMap(c kubecli.KubevirtClient) map[string]string {
	objMap := make(map[string]string)

	migs, err := c.VirtualMachineInstanceMigration(DefaultNs).List(&metav1.ListOptions{})
	if err != nil {
		return objMap
	}
	for _, mig := range migs.Items {
		objMap[mig.Spec.VMIName] = mig.Name
	}
	return objMap
}

func CleanupEvacuationObjects(c kubecli.KubevirtClient) (error, []string) {
	var objList []string
	migs, err := c.VirtualMachineInstanceMigration(DefaultNs).List(&metav1.ListOptions{})
	if err != nil {
		return err, objList
	}

	var er error
	for _, mig := range migs.Items {
		// is not running or done, clean it up
		t := mig.ObjectMeta.CreationTimestamp.Time

		if mig.IsFinal() && (time.Since(t) > time.Hour){
			objList = append(objList, mig.Name)
			mig.Finalizers = make([]string, 0)
			_, err := c.VirtualMachineInstanceMigration(DefaultNs).Update(&mig)
			if err != nil {
				er = err
				fmt.Printf("Failed to update migration object: %v, VMI:%v, err:%v", mig.Name, mig.Spec.VMIName, err)
			}
			err = c.VirtualMachineInstanceMigration(DefaultNs).Delete(mig.Name, &metav1.DeleteOptions{})
			if err != nil {
				er = err
				fmt.Printf("Failed to clean up migration object: %v, VMI:%v, err:%v", mig.Name, mig.Spec.VMIName, err)
			} else {
				fmt.Printf("Cleaned up eviction migration for VMI: %v/%v\n", mig.Spec.VMIName, mig.Name)
			}
		}
	}
	return er, objList
}

func GetVMUUID(vmi *v1.VirtualMachineInstance) string {
	owners := vmi.ObjectMeta.GetOwnerReferences()
	for _, ref := range owners {
		if ref.Kind == "VirtualMachine" {
			return string(ref.UID)
	    }
	}
	return  ""
}


type blockInfo struct {
	Blockdevices []struct {
		Name       string      `json:"name"`
	} `json:"blockdevices"`
}

func getDeviceName(volName string) (string, error) {
	name := path.Join("/var/lib/kubelet/plugins/kubernetes.io/csi/volumeDevices/publish", volName)
	out, err := exec.Command("lsblk", "-J", name).Output()
	if err != nil {
		return "", err
	}
	blkInfo := blockInfo{}
	if err = json.Unmarshal(out, &blkInfo); err == nil {
		if len(blkInfo.Blockdevices) > 0 {
			//cmd := fmt.Sprintf("multipath -l %s | grep \"active undef\" | awk '{print $3}'", blkInfo.Blockdevices[0].Name)
			name := blkInfo.Blockdevices[0].Name
			// if the name does not contain any digits, it is a non-multipath device
			if !strings.ContainsAny(name, "0123456789") {
				return name, nil
			}
			cmd := fmt.Sprintf("multipath -l %s | grep \"%s\" | awk '{print $2}'", name, name)
			out, err := exec.Command("bash", "-c", cmd).Output()
			//return strings.Split(strings.TrimSpace(string(out)), "\n"), err
			return strings.TrimSpace(string(out)), err
		} else {
			return "", fmt.Errorf("No Block devices found")
		}
	}
	return "", err
}

// PV to disk device is a mapping specific to the node on which the PV is mounted
// read it out and add it as annotation to the VMI. We'll need it
// to map the metrics back to the VM virtual disk
func AddDiskAnnoToVMIs(c kubecli.KubevirtClient, nodeName string) error {
	vmiList, err := GetVMIsOnNode(c, nodeName, DefaultNs)
	if err != nil {
		return err
	}

	for _, vmi := range vmiList.Items {
		ann := vmi.Annotations
		changed := false
		for _, v := range vmi.Spec.Volumes {
			if v.DataVolume != nil {
				pvc, err := GetPVC(c, v.DataVolume.Name)
				if err == nil {
					pvName := pvc.Spec.VolumeName
					devName, err := getDeviceName(pvName)
					if err == nil {
						key := "DiskName"+"_"+v.DataVolume.Name
						value := vmi.Status.NodeName+"_"+devName
						if ann[key] != value {
							changed = true
							ann[key] = value
						}
					} else {
						fmt.Printf("Failed to get device name for %v: %v", err, pvName)
					}
				} else {
					fmt.Printf("Error looking up PVC: %v", v.DataVolume.Name)
				}
			}
		}
		if changed {
			// annotations changed, lets update the vmi
			vmi.Annotations = ann
			_, err := c.VirtualMachineInstance(DefaultNs).Update(&vmi)
			if err != nil {
				fmt.Print("Failed to update VMI %v, err:%v", vmi.Name, err)
			}
		}
	}
	return err
}