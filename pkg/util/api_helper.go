/*
 API Extensions to enable external clients to orchestrate kubevirt VMs
*/
package util

import (
	errors2 "errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	k8sv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/rand"

	"kubevirt.io/kubevirt/pkg/api/v1"
	"kubevirt.io/kubevirt/pkg/kubecli"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"kubevirt.io/containerized-data-importer/pkg/apis/core/v1alpha1"
	cdiv1 "kubevirt.io/containerized-data-importer/pkg/apis/core/v1alpha1"
)

type DiskParams struct {
	Name, VolName, Class, FilePath, Bus string
	Svolume, SfilePath                  string
	Evolume, EfilePath                  string
	Capacity, Format                    string
	VolumeHandle                        string
	IsCdrom                             bool
	VolumeBlockMode                     bool
	HxVolName                           string
	Order                               uint
}

const pvcCreatedByVM = "vmCreated"

func NewVM(namespace string, vmName string) *v1.VirtualMachine {
	vm := &v1.VirtualMachine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      vmName,
			Namespace: namespace,
		},
		Spec: v1.VirtualMachineSpec{
			Template: &v1.VirtualMachineInstanceTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:    map[string]string{"name": vmName},
					Name:      vmName,
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
func CreateCSIPv(client kubecli.KubevirtClient, name string, params DiskParams,
	readOnly bool) (*k8sv1.PersistentVolume, error) {
	quantity, err := resource.ParseQuantity(params.Capacity)
	if err != nil {
		return nil, err
	}

	accessMode := k8sv1.ReadWriteMany
	if readOnly {
		accessMode = k8sv1.ReadOnlyMany
	}

	mode := k8sv1.PersistentVolumeFilesystem
	if params.VolumeBlockMode {
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

func UpdatePVToPrivate(client kubecli.KubevirtClient, pv *k8sv1.PersistentVolume) (*k8sv1.PersistentVolume, error) {
	pv.Spec.AccessModes = []k8sv1.PersistentVolumeAccessMode{k8sv1.ReadWriteOnce}
	return client.CoreV1().PersistentVolumes().Update(pv)
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
	pvc.Spec.AccessModes = []k8sv1.PersistentVolumeAccessMode{k8sv1.ReadWriteOnce}
	return client.CoreV1().PersistentVolumeClaims(pvc.Namespace).Update(pvc)
}

func SetResources(vm *v1.VirtualMachine, cpu, memory, cpuModel string) error {
	t := int64(30)
	vmSpec := vm.Spec.Template.Spec
	vmSpec.TerminationGracePeriodSeconds = &t
	cpus := resource.MustParse(cpu)
	vmSpec.Domain.Resources.Requests[k8sv1.ResourceMemory] = resource.MustParse(memory)
	vmSpec.Domain.Resources.Requests[k8sv1.ResourceCPU] = cpus
	cores, err := strconv.Atoi(cpu)
	if err != nil {
		return fmt.Errorf("SetResources failed: %v", err)
	}
	fmt.Printf("\nCores: %d\n", cores)
	vmSpec.Domain.CPU = &v1.CPU{Cores: uint32(cores), Model: cpuModel}
	return nil
}

// NewDataVolumeWithHTTPImport initializes a DataVolume struct with HTTP annotations
func NewDataVolumeWithHTTPImport(dataVolumeName string, size string, httpURL string) *cdiv1.DataVolume {
	return &cdiv1.DataVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: dataVolumeName,
		},
		Spec: cdiv1.DataVolumeSpec{
			Source: cdiv1.DataVolumeSource{
				HTTP: &cdiv1.DataVolumeSourceHTTP{
					URL: httpURL,
				},
			},
			PVC: &k8sv1.PersistentVolumeClaimSpec{
				AccessModes: []k8sv1.PersistentVolumeAccessMode{k8sv1.ReadWriteOnce},
				Resources: k8sv1.ResourceRequirements{
					Requests: k8sv1.ResourceList{
						k8sv1.ResourceName(k8sv1.ResourceStorage): resource.MustParse(size),
					},
				},
			},
		},
	}
}

func AttachDisk(c kubecli.KubevirtClient, vm *v1.VirtualMachine, diskParams DiskParams) error {
	disk := v1.Disk{}
	disk.Name = diskParams.Name
	disk.VolumeName = diskParams.VolName
	disk.BootOrder = &diskParams.Order
	quantity, err := resource.ParseQuantity(diskParams.Capacity)
	if err != nil {
		return err
	}
	//disk.Size = strconv.FormatInt(quantity.ToDec().ScaledValue(0), 10)

	dt := "sata"
	if diskParams.Bus == "virtio" || diskParams.Bus == "scsi" || diskParams.Bus == "ide" {
		dt = diskParams.Bus
	}

	if !diskParams.VolumeBlockMode {
		if diskParams.IsCdrom == true {
			readOnly := true
			disk.CDRom = &v1.CDRomTarget{Bus: dt, ReadOnly: &readOnly}
		} else {
			disk.Disk = &v1.DiskTarget{Bus: dt}
		}
	}

    vmSpec := vm.Spec.Template.Spec
	vmSpec.Domain.Devices.Disks = append(vmSpec.Domain.Devices.Disks, disk)

	vol := v1.Volume{}
	vol.Name = fmt.Sprintf("my-%s", diskParams.Name)
	var pvcName string
	var pvc *k8sv1.PersistentVolumeClaim
	if diskParams.HxVolName == "" {
		pvcName = fmt.Sprintf("%s-%s", diskParams.Name, "pvc")
		if diskParams.VolumeHandle == "" {
				if diskParams.SfilePath != "" {
					// datavolume
					dvTemplate := v1alpha1.DataVolume{}
					dvTemplate.ObjectMeta
					dvTemplate.Spec.Source.HTTP = &v1alpha1.DataVolumeSourceHTTP{URL: disk.SourceFilePath}
					vm.Spec.DataVolumeTemplates = append(vm.Spec.DataVolumeTemplates, dvTemplate)

					dv := NewDataVolumeWithHTTPImport(disk.Name, disk.Size, disk.SourceFilePath)
					vm.Spec.DataVolumeTemplates = append(vm.Spec.DataVolumeTemplates, dv)
				} else {
					//empty disk
					fmt.Printf("\nCreating PVC for %s, class:%v", diskParams.Name, diskParams.Class)
					pvc, err = CreateISCSIPvc(c, pvcName, "", diskParams.Class, diskParams.Capacity, "default", false,
							diskParams.VolumeBlockMode, map[string]string{pvcCreatedByVM: "yes"})
					if err != nil {
							fmt.Printf("\nFailed to create PVC %s %v", pvcName, err)
							return err
					}
				}
		} else {
			// we have a VolumeHandle from user. This is a case where a disk is being created
			// with a volumeHandle that was saved from previous creation. We will
			// create a PV using that volumeHandle and tie the PVC to this PV
			pvc, err = GetPVC(c, pvcName)
			if err != nil || pvc == nil {
				pvName := fmt.Sprintf("%s-%s", diskParams.Name, "pv")
				_, err = CreateCSIPv(c, pvName, diskParams, false)
				fmt.Printf("Creating CSI PV for %s", diskParams.Name)
				if err != nil {
					fmt.Printf("Failed to create CSI PV for %s, err: %v", diskParams.Name, err)
					return err
				}
				fmt.Printf("\nCreating PVC for %s, class:%v", diskParams.Name, diskParams.Class)
				pvc, err = CreateISCSIPvc(c, pvcName, pvName, diskParams.Class, diskParams.Capacity, "default", false,
					diskParams.VolumeBlockMode, map[string]string{pvcCreatedByVM: "yes"})
				if err != nil {
					fmt.Printf("\nFailed to create PVC %s %v", pvcName, err)
					return err
				}
			}
		}
		updatePVCPrivate(c, pvc)
		vol.PersistentVolumeClaim = &k8sv1.PersistentVolumeClaimVolumeSource{
			ClaimName: pvcName,
		}
		vmSpec.Volumes = append(vmSpec.Volumes, vol)
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
	list, err := GetVMIRSList(c, ns)
	if err != nil {
		return nil, err
	}
	for _, vmrs := range list.Items {
		if vmrs.Spec.Template.ObjectMeta.Name == vmname {
			vmList, err := GetVMIList(c, ns)
			if err != nil {
				return nil, err
			}
			for _, vmi := range vmList.Items {
				for _, ref := range vmi.OwnerReferences {
					if ref.Name == vmrs.Name {
						return &vmi, nil
					}
				}
			}
		}
	}

	errStr := fmt.Sprintf("VMI %s not found", vmname)
	return nil, errors2.New(errStr)
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

func GetPvDetails(c kubecli.KubevirtClient, name string) (bool, int32, int64, error) {
	pv, err := c.CoreV1().PersistentVolumes().Get(name, metav1.GetOptions{})
	if err != nil || pv == nil {
		return false, 0, 0, err
	}
	mode := k8sv1.PersistentVolumeFilesystem
	if pv.Spec.VolumeMode != nil {
		mode = *pv.Spec.VolumeMode
	}
	rs := pv.Spec.Capacity["storage"]
	storage, _ := (&rs).AsInt64()
	if pv.Spec.ISCSI == nil {
		return mode == k8sv1.PersistentVolumeBlock, 0, storage, nil
	}

	return mode == k8sv1.PersistentVolumeBlock, pv.Spec.ISCSI.Lun, storage, nil
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
func AddAppAffinityLables(vmi *v1.VirtualMachineInstance, affinityLabel, antiAffinityLabel map[string]string) error {

	affLables := k8sv1.Affinity{}

	if affinityLabel != nil {
		podAff := k8sv1.PodAffinity{}
		for idx, val := range affinityLabel {
			podTerm := getAffinityTerm(idx, val)
			podAff.RequiredDuringSchedulingIgnoredDuringExecution =
				append(podAff.RequiredDuringSchedulingIgnoredDuringExecution, podTerm)
		}
		affLables.PodAffinity = &podAff
	}

	if antiAffinityLabel != nil {
		podAntiAff := k8sv1.PodAntiAffinity{}
		for idx, val := range antiAffinityLabel {
			podTerm := getAffinityTerm(idx, val)
			podAntiAff.RequiredDuringSchedulingIgnoredDuringExecution =
				append(podAntiAff.RequiredDuringSchedulingIgnoredDuringExecution, podTerm)
		}
		affLables.PodAntiAffinity = &podAntiAff
	}

	vmi.Spec.Affinity = &affLables

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
	{"Created virtual machine pod ", "Create of virtual machine has been initiated"},
	{"Deleted virtual machine pod", "Stop of Virtual Machine initiated"},
	{"Deleted finished virtual machine", "Virtual machine has been stopped"},
	{"Signaled Deletion", "Virtual Machine stop operation succeeded"},
	{"VirtualMachineInstance started", "Virtual Machine started"},
	{"AttachVolume.Attach", ""},
	{"Killing container", "Virtual Machine has been stopped"},
	{"Failed to open", ""},
	{"Failed to copy", ""},
	{"Unable to mount volumes", ""},
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
		fmt.Printf("\nOld Event: %v", e)
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
						e.FirstTimestamp.Time,
						e.LastTimestamp.Time,
					}
					rC <- rEvent
				} else {
					if !strings.Contains(e.Message, lastmessage) {
						fmt.Printf("\n%v", e)
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
		if k == pvcCreatedByVM {
			return true
		}
	}

	return false
}
