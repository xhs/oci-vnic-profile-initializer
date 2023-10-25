package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

const (
	metadataVnicsEndpoint              = "http://169.254.169.254/opc/v2/vnics/"
	metadataServiceReadyTimeoutSeconds = 30
	vnicAttachmentReadyTimeoutSeconds  = 30
	profileTemplatePath                = "/etc/oci-vnic/profile.tpl"
)

var log = logrus.New()

type VnicMetadataResponse struct {
	MacAddr             string   `json:"macAddr"`
	PrivateIp           string   `json:"privateIp"`
	SubnetCidrBlock     string   `json:"subnetCidrBlock"`
	VirtualRouterIp     string   `json:"virtualRouterIp"`
	IPv6Addresses       []string `json:"ipv6Addresses,omitempty"`
	IPv6SubnetCidrBlock string   `json:"ipv6SubnetCidrBlock,omitempty"`
	IPv6VirtualRouterIp string   `json:"ipv6VirtualRouterIp,omitempty"`
}

type VnicMetadata struct {
	VnicIndex            int
	Name                 string
	MacAddr              string
	PrivateIp            string
	SubnetMaskLength     string
	VirtualRouterIp      string
	IPv6Addresses        []string
	IPv6SubnetMaskLength string
	IPv6VirtualRouterIp  string
}

func (m *VnicMetadata) RandomID() string {
	return uuid.NewString()
}

func init() {
	log.SetOutput(os.Stdout)
	log.SetFormatter(&logrus.TextFormatter{
		FullTimestamp: true,
	})
	log.SetReportCaller(true)
}

func queryVnicMacAddress(interfaceName string) (string, error) {
	log.Infof("ip link show dev %s", interfaceName)
	b, err := exec.Command("/sbin/ip", "link", "show", "dev", interfaceName).Output()
	if err != nil {
		return "", err
	}
	if len(b) == 0 {
		return "", errors.New("ip command returns empty result")
	}
	result := string(b)
	log.Infof("ip command returns: %s", result)

	lines := strings.Split(strings.TrimSpace(result), "\n")
	parts := strings.Split(strings.TrimSpace(lines[len(lines)-1]), " ")
	if len(parts) != 4 || parts[0] != "link/ether" {
		return "", errors.New("failed to parse mac address")
	}

	return strings.ToLower(parts[1]), nil
}

func doRequestVnicsMetadata() ([]*VnicMetadata, error) {
	log.Info("requesting vnics metadata")
	client := http.DefaultClient
	req, err := http.NewRequest("GET", metadataVnicsEndpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Add("Authorization", "Bearer Oracle")

	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	var vnicMetadataResponses []VnicMetadataResponse
	if err = json.NewDecoder(res.Body).Decode(&vnicMetadataResponses); err != nil {
		return nil, err
	}
	log.Infof("oci vnics metadata: %+v", vnicMetadataResponses)

	var vnicsMetadata []*VnicMetadata
	for idx, response := range vnicMetadataResponses {
		metadata := &VnicMetadata{
			VnicIndex:       idx,
			MacAddr:         strings.ToLower(response.MacAddr),
			PrivateIp:       response.PrivateIp,
			VirtualRouterIp: response.VirtualRouterIp,
		}
		parts := strings.Split(response.SubnetCidrBlock, "/")
		metadata.SubnetMaskLength = parts[len(parts)-1]

		if len(response.IPv6Addresses) > 0 {
			metadata.IPv6Addresses = make([]string, len(response.IPv6Addresses))
			copy(metadata.IPv6Addresses, response.IPv6Addresses)
			ipv6Parts := strings.Split(response.IPv6SubnetCidrBlock, "/")
			metadata.IPv6SubnetMaskLength = ipv6Parts[len(ipv6Parts)-1]
			metadata.IPv6VirtualRouterIp = response.IPv6VirtualRouterIp
		}

		vnicsMetadata = append(vnicsMetadata, metadata)
	}
	return vnicsMetadata, nil
}

func makeBackoffPolicy(maxElapsedTime time.Duration) backoff.BackOff {
	policy := backoff.NewExponentialBackOff()
	policy.InitialInterval = time.Second
	policy.Multiplier = 1.2
	policy.RandomizationFactor = 0.1
	policy.MaxElapsedTime = maxElapsedTime
	return policy
}

func requestVnicsMetadataWithRetry() ([]*VnicMetadata, error) {
	backoffPolicy := makeBackoffPolicy(time.Second * metadataServiceReadyTimeoutSeconds)
	return backoff.RetryWithData[[]*VnicMetadata](doRequestVnicsMetadata, backoffPolicy)
}

func doMatchVnicMetadata(name string, mac string) (*VnicMetadata, error) {
	log.Infof("matching vnic metadata by mac %s", mac)
	vnicsMetadata, err := requestVnicsMetadataWithRetry()
	if err != nil {
		return nil, err
	}

	for _, meta := range vnicsMetadata {
		if meta.MacAddr == mac {
			meta.Name = name
			return meta, nil
		}
	}
	return nil, errors.New("vnic metadata not matched")
}

func queryVnicMetadataWithRetry(name string, mac string) (*VnicMetadata, error) {
	backoffPolicy := makeBackoffPolicy(time.Second * vnicAttachmentReadyTimeoutSeconds)
	operation := func() (*VnicMetadata, error) {
		return doMatchVnicMetadata(name, mac)
	}
	return backoff.RetryWithData[*VnicMetadata](operation, backoffPolicy)
}

func generateVnicProfile(metadata *VnicMetadata) error {
	profileFilePath := fmt.Sprintf("/etc/sysconfig/network-scripts/ifcfg-%s", metadata.Name)
	if _, err := os.Stat(profileFilePath); err == nil {
		log.Infof("%s already exists", profileFilePath)
		return nil
	}

	log.Infof("generating %s", profileFilePath)
	t, err := template.ParseFiles(profileTemplatePath)
	if err != nil {
		return err
	}

	f, err := os.OpenFile(profileFilePath, os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	return t.Execute(f, metadata)
}

func main() {
	if len(os.Args) != 2 {
		log.Fatal("invalid number of arguments")
	}
	interfaceName := os.Args[1]

	mac, err := queryVnicMacAddress(interfaceName)
	if err != nil {
		log.Fatal(err)
	}

	metadata, err := queryVnicMetadataWithRetry(interfaceName, mac)
	if err != nil {
		log.Fatal(err)
	}
	log.Infof("vnic metadata matched: %+v", metadata)

	if err := generateVnicProfile(metadata); err != nil {
		log.Fatal(err)
	}
}
