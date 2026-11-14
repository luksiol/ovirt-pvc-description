
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// Disk represents an oVirt disk.
type Disk struct {
	ID          string `xml:"id,attr"`
	Name        string `xml:"name"`
	Description string `xml:"description"`
}

// Disks represents the oVirt disks API response.
type Disks struct {
	Items []Disk `xml:"disk"`
}

// OVirtClient handles communication with oVirt API.
type OVirtClient struct {
	BaseURL  string
	Username string
	Password string
	HTTP     *http.Client
}

// NewOVirtClient creates a new oVirt API client.
func NewOVirtClient() (*OVirtClient, error) {
	baseURL := strings.TrimRight(os.Getenv("OVIRT_URL"), "/")
	username := os.Getenv("OVIRT_USERNAME")
	password := os.Getenv("OVIRT_PASSWORD")

	if baseURL == "" || username == "" || password == "" {
		return nil, fmt.Errorf(
			"OVIRT_URL, OVIRT_USERNAME and OVIRT_PASSWORD are required",
		)
	}

	insecure := strings.EqualFold(
		strings.TrimSpace(os.Getenv("OVIRT_INSECURE_SKIP_VERIFY")),
		"true",
	)

	tlsConfig := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: insecure, // Deliberately controlled by the Secret setting.
	}

	if insecure {
		log.Println("WARNING: oVirt TLS certificate verification is DISABLED")
	} else {
		caFile := strings.TrimSpace(os.Getenv("OVIRT_CA_FILE"))

		if caFile != "" {
			caPEM, err := os.ReadFile(caFile)
			if err != nil {
				return nil, fmt.Errorf("read oVirt CA certificate %q: %w", caFile, err)
			}

			rootCAs, err := x509.SystemCertPool()
			if err != nil || rootCAs == nil {
				rootCAs = x509.NewCertPool()
			}

			if ok := rootCAs.AppendCertsFromPEM(caPEM); !ok {
				return nil, fmt.Errorf("no valid CA certificates found in %q", caFile)
			}

			tlsConfig.RootCAs = rootCAs
			log.Printf("oVirt TLS: using CA certificate from %s", caFile)
		} else {
			log.Println("oVirt TLS: using system certificate authorities")
		}
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = tlsConfig

	return &OVirtClient{
		BaseURL:  baseURL,
		Username: username,
		Password: password,
		HTTP: &http.Client{
			Timeout:   30 * time.Second,
			Transport: transport,
		},
	}, nil
}

// request performs an authenticated request to the oVirt API.
func (o *OVirtClient) request(
	ctx context.Context,
	method string,
	path string,
	body io.Reader,
) (*http.Response, error) {
	req, err := http.NewRequestWithContext(
		ctx,
		method,
		o.BaseURL+path,
		body,
	)
	if err != nil {
		return nil, err
	}

	req.SetBasicAuth(o.Username, o.Password)
	req.Header.Set("Accept", "application/xml")

	if method == http.MethodPut {
		req.Header.Set("Content-Type", "application/xml")
	}

	return o.HTTP.Do(req)
}

// ListDisks retrieves all disks from oVirt.
func (o *OVirtClient) ListDisks(ctx context.Context) ([]Disk, error) {
	resp, err := o.request(ctx, http.MethodGet, "/disks", nil)
	if err != nil {
		return nil, fmt.Errorf("oVirt GET disks request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf(
			"oVirt GET disks: HTTP %d: %s",
			resp.StatusCode,
			string(data),
		)
	}

	var result Disks
	if err := xml.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode oVirt disks: %w", err)
	}

	return result.Items, nil
}

// UpdateDescription updates the description of an oVirt disk.
func (o *OVirtClient) UpdateDescription(
	ctx context.Context,
	diskID string,
	description string,
) error {
	payload := struct {
		XMLName     xml.Name `xml:"disk"`
		Description string   `xml:"description"`
	}{
		Description: description,
	}

	body, err := xml.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal disk description: %w", err)
	}

	path := "/disks/" + url.PathEscape(diskID)

	resp, err := o.request(
		ctx,
		http.MethodPut,
		path,
		strings.NewReader(xml.Header+string(body)),
	)
	if err != nil {
		return fmt.Errorf("oVirt PUT disk %s request: %w", diskID, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(resp.Body)
		return fmt.Errorf(
			"oVirt PUT disk %s: HTTP %d: %s",
			diskID,
			resp.StatusCode,
			string(data),
		)
	}

	return nil
}

// desiredDescription creates the description that should be stored in oVirt.
func desiredDescription(pvc *corev1.PersistentVolumeClaim) string {
	pvName := pvc.Spec.VolumeName
	if pvName == "" {
		pvName = "not-bound"
	}

	storageClass := "none"
	if pvc.Spec.StorageClassName != nil {
		storageClass = *pvc.Spec.StorageClassName
	}

	descprefix := os.Getenv("DESCRIPTION_PREFIX")
	updateTime := time.Now().Format(time.RFC3339)

	return fmt.Sprintf(
		"%s | Namespace: %s | PVC: %s | StorageClass: %s | Last update: %s",
                descprefix,
		pvc.Namespace,
		pvc.Name,
		storageClass,
		updateTime,
	)
}

// reconcile synchronizes Kubernetes PVCs with oVirt disks.
func reconcile(
	ctx context.Context,
	kube kubernetes.Interface,
	ovirt *OVirtClient,
) error {
	pvcs, err := kube.CoreV1().PersistentVolumeClaims("").List(
		ctx,
		metav1.ListOptions{},
	)
	if err != nil {
		return fmt.Errorf("list PVCs: %w", err)
	}

	disks, err := ovirt.ListDisks(ctx)
	if err != nil {
		return fmt.Errorf("list oVirt disks: %w", err)
	}

	diskMap := make(map[string]Disk, len(disks))
	for _, disk := range disks {
		diskMap[disk.Name] = disk
	}

	log.Printf(
		"Found %d PVCs and %d oVirt disks",
		len(pvcs.Items),
		len(disks),
	)

	updated := 0
	unchanged := 0
	notFound := 0
	skipped := 0
	failed := 0

	for i := range pvcs.Items {
		pvc := &pvcs.Items[i]

		if pvc.Status.Phase != corev1.ClaimBound {
			skipped++
			continue
		}

		// The oVirt disk name matches the Kubernetes PV name,
		// not the PVC name.
		diskName := pvc.Spec.VolumeName

		if diskName == "" {
			log.Printf("PVC has no PV name: %s/%s", pvc.Namespace, pvc.Name)
			skipped++
			continue
		}

		disk, found := diskMap[diskName]
		if !found {
			log.Printf(
				"oVirt disk not found: %s (PVC %s/%s)",
				diskName,
				pvc.Namespace,
				pvc.Name,
			)
			notFound++
			continue
		}

		description := desiredDescription(pvc)

		if disk.Description == description {
			log.Printf("Disk already synchronized: %s", disk.Name)
			unchanged++
			continue
		}

		log.Printf(
			"Updating disk %s (ID: %s) for PVC %s/%s",
			disk.Name,
			disk.ID,
			pvc.Namespace,
			pvc.Name,
		)

		if err := ovirt.UpdateDescription(ctx, disk.ID, description); err != nil {
			log.Printf("ERROR updating disk %s: %v", disk.Name, err)
			failed++
			continue
		}

		log.Printf("Successfully updated disk: %s", disk.Name)
		updated++
	}

	log.Printf(
		"Reconciliation completed: updated=%d unchanged=%d notFound=%d skipped=%d failed=%d",
		updated,
		unchanged,
		notFound,
		skipped,
		failed,
	)

	return nil
}

func main() {
	log.Println("Starting oVirt PVC controller")

	config, err := rest.InClusterConfig()
	if err != nil {
		log.Fatalf("Kubernetes in-cluster config: %v", err)
	}

	kube, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Fatalf("Create Kubernetes client: %v", err)
	}

	ovirt, err := NewOVirtClient()
	if err != nil {
		log.Fatalf("Create oVirt client: %v", err)
	}

	interval := 60 * time.Second

	if value := os.Getenv("RESYNC_INTERVAL"); value != "" {
		parsed, err := time.ParseDuration(value)
		if err != nil || parsed <= 0 {
			log.Fatalf("Invalid RESYNC_INTERVAL: %s", value)
		}
		interval = parsed
	}

	log.Printf("Controller configuration: resync interval=%s", interval)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		log.Println("Starting reconciliation")

		if err := reconcile(ctx, kube, ovirt); err != nil {
			log.Printf("Reconciliation error: %v", err)
		}

		log.Printf("Next reconciliation in %s", interval)

		select {
		case <-ctx.Done():
			log.Println("Controller stopped")
			return
		case <-ticker.C:
		}
	}
}


