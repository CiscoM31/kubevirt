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
)

type DiskParams struct {
	Name, VolName, Class, FilePath, Bus string
	Svolume, SfilePath                  string
	Evolume, EfilePath                  string
	Capacity, Format                    string
	IsCdrom                             bool
	VolumeBlockMode                     bool
	HxVolName                           string
}

const pvcCreatedByVM = "vmCreated"

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

func CreateISCSIPvc(client kubecli.KubevirtClient, name, class, capacity, ns string, readOnly, volumeBlockMode bool,
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

func SetResources(vmi *v1.VirtualMachineInstance, cpu, memory, cpuModel string) error {
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
	vmi.Spec.Domain.CPU = &v1.CPU{Cores: uint32(cores), Model: cpuModel}
	return nil
}

func AttachDisk(c kubecli.KubevirtClient, vmi *v1.VirtualMachineInstance, diskParams DiskParams) error {
	disk := v1.Disk{}
	disk.Name = diskParams.Name
	disk.VolumeName = diskParams.VolName

	quantity, err := resource.ParseQuantity(diskParams.Capacity)
	if err != nil {
		return err
	}
	disk.Size = strconv.FormatInt(quantity.ToDec().ScaledValue(0), 10)

	dt := "sata"
	if diskParams.Bus == "virtio" || diskParams.Bus == "scsi" || diskParams.Bus == "ide" {
		dt = diskParams.Bus
	}

	if !diskParams.VolumeBlockMode {
		disk.FilePath = diskParams.FilePath
		disk.SourceFilePath = diskParams.SfilePath
		disk.SourceVolumeName = diskParams.Svolume
		disk.ExportVolumeName = diskParams.Evolume
		disk.ExportFilePath = diskParams.EfilePath
		if diskParams.IsCdrom == true {
			readOnly := true
			disk.CDRom = &v1.CDRomTarget{Bus: dt, ReadOnly: &readOnly}
		} else {
			disk.Disk = &v1.DiskTarget{Bus: dt, ImageFormat: diskParams.Format}
		}

	}

	vmi.Spec.Domain.Devices.Disks = append(vmi.Spec.Domain.Devices.Disks, disk)

	vol := v1.Volume{}
	vol.Name = fmt.Sprintf("my-%s", diskParams.Name)
	var pvcName string
	var pvc *k8sv1.PersistentVolumeClaim
	if diskParams.HxVolName == "" {
		fmt.Printf("\nCreating PVC for %s, class:%v", diskParams.Name, diskParams.Class)
		pvcName = fmt.Sprintf("%s-%s", diskParams.Name, "pvc")
		pvc, err = CreateISCSIPvc(c, pvcName, diskParams.Class, diskParams.Capacity, "default", false,
			diskParams.VolumeBlockMode, map[string]string{pvcCreatedByVM: "yes"})
		if err != nil {
			fmt.Printf("\nFailed to create PVC %s %v", pvcName, err)
			return err
		}
		updatePVCPrivate(c, pvc)
	} else {
		pvcName = diskParams.HxVolName
		if ok, _ := IsHXVolumeAvailable(c, diskParams.HxVolName); !ok {
			fmt.Printf("HX Volume %s not found", pvcName)
			return fmt.Errorf("HX Volume %s not found", pvcName)
		}
	}

	vol.PersistentVolumeClaim = &k8sv1.PersistentVolumeClaimVolumeSource{
		ClaimName: pvcName,
	}
	vmi.Spec.Volumes = append(vmi.Spec.Volumes, vol)

	if diskParams.Svolume != "" {
		vol = v1.Volume{}
		vol.Name = fmt.Sprintf("%s", diskParams.Svolume)
		pvcName = fmt.Sprintf("%s-%s", vol.Name, "source-pvc")
		fmt.Printf("\nCreating source PVC for %s", pvcName)
		sClass := fmt.Sprintf("%s-class", diskParams.Svolume)

		pV, err := GetPv(c, diskParams.Svolume)
		if err != nil {
			fmt.Printf("PV %s not found for PVC %s configuration", diskParams.Svolume, pvcName)
			return err
		}
		accessMode := pV.Spec.AccessModes
		readOnly := false
		if len(accessMode) != 0 {
			if accessMode[0] == k8sv1.ReadOnlyMany {
				readOnly = true
			}
		}
		pvc, err = CreateISCSIPvc(c, pvcName, sClass, diskParams.Capacity, "default", readOnly,
			diskParams.VolumeBlockMode, map[string]string{pvcCreatedByVM: "yes"})
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

	if diskParams.Evolume != "" {
		vol = v1.Volume{}
		vol.Name = fmt.Sprintf("%s", diskParams.Evolume)
		pvcName = fmt.Sprintf("%s-%s", vol.Name, "export-pvc")
		fmt.Printf("\nCreating export PVC for %s", pvcName)
		eClass := fmt.Sprintf("%s-class", diskParams.Evolume)

		pV, err := GetPv(c, diskParams.Evolume)
		if err != nil {
			fmt.Printf("PV %s not found for PVC %s configuration", diskParams.Evolume, pvcName)
			return err
		}
		accessMode := pV.Spec.AccessModes
		readOnly := false
		if len(accessMode) != 0 {
			if accessMode[0] == k8sv1.ReadOnlyMany {
				readOnly = true
			}
		}
		pvc, err = CreateISCSIPvc(c, pvcName, eClass, diskParams.Capacity, "default", readOnly,
			diskParams.VolumeBlockMode, map[string]string{pvcCreatedByVM: "yes"})
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

func GetPv(c kubecli.KubevirtClient, name string) (*k8sv1.PersistentVolume, error) {
	pv, err := c.CoreV1().PersistentVolumes().Get(name, metav1.GetOptions{})
	if err != nil || pv == nil {
		return nil, err
	}
	return pv, err
}

func GetPvDetails(c kubecli.KubevirtClient, name string) (bool, int32, string, error) {
	pv, err := c.CoreV1().PersistentVolumes().Get(name, metav1.GetOptions{})
	if err != nil || pv == nil {
		return false, 0, "", err
	}
	mode := k8sv1.PersistentVolumeFilesystem
	if pv.Spec.VolumeMode != nil {
		mode = *pv.Spec.VolumeMode
	}
	rs := pv.Spec.Capacity["storage"]
	storage := (&rs).String()
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
