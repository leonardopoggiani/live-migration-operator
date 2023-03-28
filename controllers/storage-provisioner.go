package controllers

import (
	"bufio"
	"context"
	"fmt"
	netv1alpha1 "github.com/liqotech/liqo/apis/net/v1alpha1"
	liqoconst "github.com/liqotech/liqo/pkg/consts"
	"github.com/liqotech/liqo/pkg/liqonet/tunnel/resolver"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/json"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
	"net"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/sig-storage-lib-external-provisioner/v7/controller"
	"sync"
	"time"
)

const (
	port     = 12345
	buffSize = 1024
)

var (
	// PingLossThreshold is the number of lost packets after which the connection check is considered as failed.
	PingLossThreshold uint
	// PingInterval is the interval at which the ping is sent.
	PingInterval time.Duration
)

type CheckpointProvisioner struct {
	client                  client.Client
	virtualStorageClassName string
	storageNamespace        string
	localRealStorageClass   string
}

func (c CheckpointProvisioner) Provision(ctx context.Context, options controller.ProvisionOptions) (*corev1.PersistentVolume, controller.ProvisioningState, error) {
	// Create a new PV object based on the PVC's specifications
	klog.Infof("Provisioning a new PV for PVC %s/%s", options.PVC.Namespace, options.PVC.Name)

	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: options.PVC.Name,
		},
		Spec: corev1.PersistentVolumeSpec{
			AccessModes: options.PVC.Spec.AccessModes,
			Capacity:    options.PVC.Spec.Resources.Requests,
			ClaimRef: &corev1.ObjectReference{
				APIVersion: "v1",
				Kind:       "PersistentVolumeClaim",
				Name:       options.PVC.Name,
				Namespace:  options.PVC.Namespace,
			},
			PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimDelete,
			StorageClassName:              options.StorageClass.GetName(),
			// MountOptions:                  options.Parameters["mountOptions"],
			VolumeMode: options.PVC.Spec.VolumeMode,
			// Set the checkpoint storage location based on the provisioner's configuration

			// NFS: &corev1.NFSVolumeSource{
			// 	Server: "liqo-gateway-service.remote-cluster.svc.cluster.local",
			//	Path:   "/mnt/checkpoints",
			//},
		},
	}

	// Create the new PV object in Kubernetes
	if err := c.client.Create(ctx, pv); err != nil {
		klog.Errorf("Failed to create PV %s: %v", pv.Name, err)
		return nil, controller.ProvisioningFinished, err
	}

	// TODO: send the checkpoint to the remote cluster
	// need to open a socket to the liqo gateway service on the remote cluster
	// remote gateway service address
	var tep = new(netv1alpha1.TunnelEndpoint)
	endpoint, err := getEndpoint(tep, func(address string) (*net.IPAddr, error) {
		return resolver.Resolve(context.TODO(), address)
	})

	// Step 2: Create a TCP connection to the endpoint IP address.
	conn, err := net.Dial("tcp", endpoint.String())

	// Step 3: Create a bufio reader and writer.
	writer := bufio.NewWriter(conn)
	reader := bufio.NewReader(conn)

	// Step 4: Write the checkpoint data to the TCP connection.
	checkpointData := "checkpoint data"
	_, err = writer.WriteString(checkpointData)
	if err != nil {
		// Handle error.
	}

	// Step 5: Read the response from the remote endpoint.
	response, err := reader.ReadString('\n')
	if err != nil {
		// Handle error.
	} else {
		klog.ErrorS(err, "response", "response", response)
	}

	// Step 6: Close the TCP connection.
	err = conn.Close()
	if err != nil {
		// Handle error.
	}

	return pv, controller.ProvisioningFinished, nil
}

func (c CheckpointProvisioner) Delete(ctx context.Context, volume *corev1.PersistentVolume) error {
	//TODO implement me
	panic("implement me")
}

func NewCheckpointProvisioner(ctx context.Context, cl client.Client, storageNamespace string) (controller.Provisioner, error) {
	// ensure that the storage namespace exists
	err := cl.Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: storageNamespace,
		},
	})

	klog.Error(err)

	return &CheckpointProvisioner{
		client:                  cl,
		virtualStorageClassName: "",
		storageNamespace:        storageNamespace,
		localRealStorageClass:   "",
	}, nil
}

type LiqoNodeProvider struct {
	localClient           kubernetes.Interface
	remoteDiscoveryClient discovery.DiscoveryInterface
	dynClient             dynamic.Interface

	node              *corev1.Node
	terminating       bool
	lastAppliedLabels map[string]string

	nodeName         string
	foreignClusterID string
	tenantNamespace  string
	resyncPeriod     time.Duration
	pingDisabled     bool

	networkReady bool

	onNodeChangeCallback func(*corev1.Node)
	updateMutex          sync.Mutex
}

type Sender struct {
	clusterID string
	cancel    func()
	conn      *net.UDPConn
	raddr     net.UDPAddr
}

// NewSender creates a new conncheck sender.
func NewSender(ctx context.Context, clusterID string, cancel func(), conn *net.UDPConn, ip string) *Sender {
	return &Sender{
		clusterID: clusterID,
		cancel:    cancel,
		conn:      conn,
		raddr:     net.UDPAddr{IP: net.ParseIP(ip), Port: port},
	}
}

// Ping checks if the the node is still active.
func (p *LiqoNodeProvider) Ping(ctx context.Context) error {
	if p.pingDisabled {
		return nil
	}

	start := time.Now()
	klog.V(4).Infof("Checking whether the remote API server is ready")

	_, err := p.remoteDiscoveryClient.RESTClient().Get().AbsPath("/livez").DoRaw(ctx)
	if err != nil {
		klog.Errorf("API server readiness check failed: %v", err)
		return err
	}

	klog.V(4).Infof("Readiness check completed successfully in %v", time.Since(start))
	return nil
}

// SendPing sends a PING message to the given address.
func (s *Sender) SendPing(ctx context.Context) error {
	msgOut := Msg{ClusterID: s.clusterID, MsgType: PING, TimeStamp: time.Now()}
	b, err := json.Marshal(msgOut)
	if err != nil {
		return fmt.Errorf("conncheck sender: failed to marshal msg: %w", err)
	}
	_, err = s.conn.WriteToUDP(b, &s.raddr)
	if err != nil {
		return fmt.Errorf("conncheck sender: failed to write to %s: %w", s.raddr.String(), err)
	}
	klog.V(8).Infof("conncheck sender: sent a PING -> %s", msgOut)
	return nil
}

type Msg struct {
	ClusterID string    `json:"clusterID"`
	MsgType   MsgTypes  `json:"msgType"`
	TimeStamp time.Time `json:"timeStamp"`
}

func (msg Msg) String() string {
	return fmt.Sprintf("ClusterID: %s, MsgType: %s, Timestamp: %s",
		msg.ClusterID,
		msg.MsgType,
		msg.TimeStamp.Format("00:00:00.000000000"))
}

// MsgTypes represents the type of a message.
type MsgTypes string

const (
	// PING is the type of a ping message.
	PING MsgTypes = "PING"
	// PONG is the type of a pong message.
	PONG MsgTypes = "PONG"
)

func getEndpoint(tep *netv1alpha1.TunnelEndpoint, addrResolver ResolverFunc) (*net.UDPAddr, error) {
	// Get tunnel port.
	tunnelPort, err := getTunnelPortFromTep(tep)
	if err != nil {
		return nil, err
	}
	// Get tunnel ip.
	tunnelAddress, err := getTunnelAddressFromTep(tep, addrResolver)
	if err != nil {
		return nil, err
	}
	return &net.UDPAddr{
		IP:   tunnelAddress.IP,
		Port: tunnelPort,
	}, nil
}

func getTunnelPortFromTep(tep *netv1alpha1.TunnelEndpoint) (int, error) {
	// Get port.
	port, found := tep.Spec.BackendConfig[liqoconst.ListeningPort]
	if !found {
		return 0, fmt.Errorf("port not found in BackendConfig map using key {%s}", liqoconst.ListeningPort)
	}
	// Convert port from string to int.
	tunnelPort, err := strconv.ParseInt(port, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("unable to parse port {%s} to int: %w", port, err)
	}
	// If port is not in the correct range, then return an error.
	if tunnelPort < liqoconst.UDPMinPort || tunnelPort > liqoconst.UDPMaxPort {
		return 0, fmt.Errorf("port {%s} should be greater than {%d} and minor than {%d}", port, liqoconst.UDPMinPort, liqoconst.UDPMaxPort)
	}
	return int(tunnelPort), nil
}

func getTunnelAddressFromTep(tep *netv1alpha1.TunnelEndpoint, addrResolver ResolverFunc) (*net.IPAddr, error) {
	return addrResolver(tep.Spec.EndpointIP)
}

type ResolverFunc func(address string) (*net.IPAddr, error)
