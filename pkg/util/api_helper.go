/*
 API Extensions to enable external clients to orchestrate kubevirt VMs
*/
package util

import (
	"fmt"
	"strconv"

	k8sv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/rand"

	errors2 "errors"

	"strings"

	"kubevirt.io/kubevirt/pkg/api/v1"
	"kubevirt.io/kubevirt/pkg/kubecli"
)

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

func CreateISCSIPv(client kubecli.KubevirtClient, name, class, capacity, target, iqn string, lun int32, volBlockMode bool) (*k8sv1.PersistentVolume, error) {
	quantity, err := resource.ParseQuantity(capacity)
	if err != nil {
		return nil, err
	}

	mode := k8sv1.PersistentVolumeFilesystem
	if volBlockMode {
		mode = k8sv1.PersistentVolumeBlock
	}
	pv := &k8sv1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: k8sv1.PersistentVolumeSpec{
			AccessModes: []k8sv1.PersistentVolumeAccessMode{k8sv1.ReadWriteMany},
			Capacity: k8sv1.ResourceList{
				"storage": quantity,
			},
			PersistentVolumeReclaimPolicy: k8sv1.PersistentVolumeReclaimRetain,
			PersistentVolumeSource: k8sv1.PersistentVolumeSource{
				ISCSI: &k8sv1.ISCSIPersistentVolumeSource{
					TargetPortal: target,
					IQN:          iqn,
					Lun:          lun,
					ReadOnly:     false,
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

func UpdatePVToPrivate(client kubecli.KubevirtClient, pv *k8sv1.PersistentVolume) (*k8sv1.PersistentVolume, error) {
	pv.Spec.AccessModes = []k8sv1.PersistentVolumeAccessMode{k8sv1.ReadWriteOnce}
	return client.CoreV1().PersistentVolumes().Update(pv)
}

func CreateISCSIPvc(client kubecli.KubevirtClient, name, class, capacity, ns string, volumeBlockMode bool) (*k8sv1.PersistentVolumeClaim, error) {
	quantity, err := resource.ParseQuantity(capacity)
	if err != nil {
		return nil, err
	}
	mode := k8sv1.PersistentVolumeFilesystem
	if volumeBlockMode {
		mode = k8sv1.PersistentVolumeBlock
	}

	pvc := &k8sv1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: k8sv1.PersistentVolumeClaimSpec{
			AccessModes: []k8sv1.PersistentVolumeAccessMode{k8sv1.ReadWriteMany},
			Resources: k8sv1.ResourceRequirements{
				Requests: k8sv1.ResourceList{
					"storage": quantity,
				},
			},
			StorageClassName: &class,
			VolumeMode:       &mode,
		},
	}

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
	class := pvc.Spec.StorageClassName
	pvName := strings.Split(*class, "-class")
	if len(pvName) <= 0 {
		return "", fmt.Errorf("failed to split pvc class name")
	}
	return pvName[0], nil
}

func ListPVs(client kubecli.KubevirtClient) (*k8sv1.PersistentVolumeList, error) {
	return client.CoreV1().PersistentVolumes().List(metav1.ListOptions{})
}

func DeletePV(client kubecli.KubevirtClient, name string) error {
	return client.CoreV1().PersistentVolumes().Delete(name, &metav1.DeleteOptions{})
}

func UpdatePVCPrivate(client kubecli.KubevirtClient, pvc *k8sv1.PersistentVolumeClaim) (*k8sv1.PersistentVolumeClaim, error) {
	pvc.Spec.AccessModes = []k8sv1.PersistentVolumeAccessMode{k8sv1.ReadWriteOnce}
	return client.CoreV1().PersistentVolumeClaims(pvc.Namespace).Update(pvc)
}

func SetResources(vmi *v1.VirtualMachineInstance, cpu, memory string) error {
	t := int64(30)
	vmi.Spec.TerminationGracePeriodSeconds = &t
	cpus := resource.MustParse(cpu)
	vmi.Spec.Domain.Resources.Requests[k8sv1.ResourceMemory] = resource.MustParse(memory)
	vmi.Spec.Domain.Resources.Requests[k8sv1.ResourceCPU] = cpus
	os := v1.OS{BootOrder: "hd,cdrom"}
	vmi.Spec.Domain.OS = &os
	cores, err := strconv.Atoi(cpu)
	if err != nil {
		return fmt.Errorf("SetResources failed: %v", err)
	}
	fmt.Printf("\nCores: %d\n", cores)
	vmi.Spec.Domain.CPU = &v1.CPU{Cores: uint32(cores), Model: "host-model"}
	return nil
}

func AttachDisk(c kubecli.KubevirtClient, vmi *v1.VirtualMachineInstance, diskName, volumeName, class,
	filepath, sVolume, sfilepath, capacity, format string, isCdrom, volumeBlockMode bool) error {
	disk := v1.Disk{}
	disk.Name = diskName
	disk.VolumeName = volumeName

	if !volumeBlockMode {
		disk.FilePath = filepath
		disk.SourceFilePath = sfilepath
		disk.SourceVolumeName = sVolume

		if isCdrom == true {
			readOnly := true
			disk.CDRom = &v1.CDRomTarget{Bus: "sata", ReadOnly: &readOnly}
		} else {
			disk.Disk = &v1.DiskTarget{Bus: "sata", ImageFormat: format}
		}

	}

	vmi.Spec.Domain.Devices.Disks = append(vmi.Spec.Domain.Devices.Disks, disk)

	vol := v1.Volume{}
	vol.Name = fmt.Sprintf("my-%s", diskName)

	fmt.Printf("\nCreating PVC for %s, class:%v", diskName, class)
	pvcName := fmt.Sprintf("%s-%s", diskName, "pvc")
	pvc, err := CreateISCSIPvc(c, pvcName, class, capacity, "default", volumeBlockMode)
	if err != nil {
		fmt.Printf("\nFailed to create PVC %s %v", pvcName, err)
		return err
	}
	UpdatePVCPrivate(c, pvc)

	vol.PersistentVolumeClaim = &k8sv1.PersistentVolumeClaimVolumeSource{
		ClaimName: pvc.Name,
	}
	vmi.Spec.Volumes = append(vmi.Spec.Volumes, vol)

	if sVolume != "" {
		vol = v1.Volume{}
		vol.Name = fmt.Sprintf("%s", sVolume)
		pvcName = fmt.Sprintf("%s-%s", vol.Name, "source-pvc")
		fmt.Printf("\nCreating source PVC for %s", pvcName)
		sClass := fmt.Sprintf("%s-class", sVolume)
		pvc, err = CreateISCSIPvc(c, pvcName, sClass, capacity, "default", volumeBlockMode)
		if err != nil {
			fmt.Printf("\nFailed to create PVC %s %v", pvcName, err)
			return err
		}

		vol.PersistentVolumeClaim = &k8sv1.PersistentVolumeClaimVolumeSource{
			ClaimName: pvc.Name,
		}
		for _, v := range vmi.Spec.Volumes {
			// if already in the list, this is probably multiple disks refering
			// to same source volume
			if v.Name == vol.Name {
				return nil
			}
		}
		vmi.Spec.Volumes = append(vmi.Spec.Volumes, vol)
	}

	return nil
}

func GetVMIList(c kubecli.KubevirtClient, ns string) (*v1.VirtualMachineInstanceList, error) {
	vmis, err := c.VirtualMachineInstance(ns).List(&metav1.ListOptions{})
	return vmis, err
}

func GetVMIRSList(c kubecli.KubevirtClient, ns string) (*v1.VirtualMachineInstanceReplicaSetList, error) {
	vmis, err := c.ReplicaSet(ns).List(metav1.ListOptions{})
	return vmis, err
}

func GetVMIRS(c kubecli.KubevirtClient, vminame, ns string) (*v1.VirtualMachineInstanceReplicaSet, error) {
	list, err := GetVMIRSList(c, ns)
	if err != nil {
		return nil, err
	}
	for _, vmirs := range list.Items {
		if vmirs.Spec.Template.ObjectMeta.Name == vminame {
			return &vmirs, nil
		}
	}

	return nil, fmt.Errorf("VMI Replicaset %s not found", vminame)
}

func GetVMI(c kubecli.KubevirtClient, vminame, ns string) (*v1.VirtualMachineInstance, error) {
	list, err := GetVMIRSList(c, ns)
	if err != nil {
		return nil, err
	}
	for _, vmirs := range list.Items {
		if vmirs.Spec.Template.ObjectMeta.Name == vminame {
			vmiList, err := GetVMIList(c, ns)
			if err != nil {
				return nil, err
			}
			for _, vmi := range vmiList.Items {
				for _, ref := range vmi.OwnerReferences {
					if ref.Name == vmirs.Name {
						return &vmi, nil
					}
				}
			}
		}
	}

	errStr := fmt.Sprintf("VMI %s not found", vminame)
	return nil, errors2.New(errStr)
}

func DeleteVMIRS(c kubecli.KubevirtClient, name, ns string) error {
	return c.ReplicaSet(ns).Delete(name, &metav1.DeleteOptions{})
}

func DeletePVC(c kubecli.KubevirtClient, name, ns string) error {
	return c.CoreV1().PersistentVolumeClaims(ns).Delete(name, &metav1.DeleteOptions{})
}
