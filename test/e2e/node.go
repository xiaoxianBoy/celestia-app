package e2e

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/celestiaorg/knuu/pkg/knuu"
	serverconfig "github.com/cosmos/cosmos-sdk/server/config"
	"github.com/tendermint/tendermint/config"
	"github.com/tendermint/tendermint/crypto"
	"github.com/tendermint/tendermint/p2p"
	"github.com/tendermint/tendermint/privval"
	"github.com/tendermint/tendermint/rpc/client/http"
	"github.com/tendermint/tendermint/types"
)

const (
	rpcPort       = 26657
	p2pPort       = 26656
	grpcPort      = 9090
	metricsPort   = 26660
	dockerSrcURL  = "ghcr.io/celestiaorg/celestia-app"
	secp256k1Type = "secp256k1"
	ed25519Type   = "ed25519"
	remoteRootDir = "/home/celestia/.celestia-app"
)

type Node struct {
	Name           string
	Version        string
	StartHeight    int64
	InitialPeers   []string
	SignerKey      crypto.PrivKey
	NetworkKey     crypto.PrivKey
	AccountKey     crypto.PrivKey
	SelfDelegation int64
	Instance       *knuu.Instance

	rpcProxyPort  int
	grpcProxyPort int
}

func NewNode(
	name, version string,
	startHeight, selfDelegation int64,
	peers []string,
	signerKey, networkKey, accountKey crypto.PrivKey,
	upgradeHeight int64,
) (*Node, error) {
	instance, err := knuu.NewInstance(name)
	if err != nil {
		return nil, err
	}
	err = instance.SetImage(DockerImageName(version))
	if err != nil {
		return nil, err
	}
	if err := instance.AddPortTCP(rpcPort); err != nil {
		return nil, err
	}
	if err := instance.AddPortTCP(p2pPort); err != nil {
		return nil, err
	}
	if err := instance.AddPortTCP(grpcPort); err != nil {
		return nil, err
	}
	if err := instance.AddPortUDP(metricsPort); err != nil {
		return nil, err
	}
	err = instance.SetMemory("200Mi", "200Mi")
	if err != nil {
		return nil, err
	}
	err = instance.SetCPU("300m")
	if err != nil {
		return nil, err
	}
	err = instance.AddVolumeWithOwner(remoteRootDir, "1Gi", 10001)
	if err != nil {
		return nil, err
	}
	args := []string{"start", fmt.Sprintf("--home=%s", remoteRootDir), "--rpc.laddr=tcp://0.0.0.0:26657"}
	if upgradeHeight != 0 {
		args = append(args, fmt.Sprintf("--v2-upgrade-height=%d", upgradeHeight))
	}

	err = instance.SetArgs(args...)
	if err != nil {
		return nil, err
	}

	return &Node{
		Name:           name,
		Instance:       instance,
		Version:        version,
		StartHeight:    startHeight,
		InitialPeers:   peers,
		SignerKey:      signerKey,
		NetworkKey:     networkKey,
		AccountKey:     accountKey,
		SelfDelegation: selfDelegation,
	}, nil
}

func (n *Node) Init(genesis types.GenesisDoc, peers []string) error {
	if len(peers) == 0 {
		return fmt.Errorf("no peers provided")
	}

	// Initialize file directories
	rootDir := os.TempDir()
	nodeDir := filepath.Join(rootDir, n.Name)
	for _, dir := range []string{
		filepath.Join(nodeDir, "config"),
		filepath.Join(nodeDir, "data"),
	} {
		if err := os.MkdirAll(dir, os.ModePerm); err != nil {
			return fmt.Errorf("error creating directory %s: %w", dir, err)
		}
	}

	// Create and write the config file
	cfg, err := MakeConfig(n)
	if err != nil {
		return fmt.Errorf("making config: %w", err)
	}
	configFilePath := filepath.Join(nodeDir, "config", "config.toml")
	config.WriteConfigFile(configFilePath, cfg)

	// Store the genesis file
	genesisFilePath := filepath.Join(nodeDir, "config", "genesis.json")
	err = genesis.SaveAs(genesisFilePath)
	if err != nil {
		return fmt.Errorf("saving genesis: %w", err)
	}

	// Create the app.toml file
	appConfig, err := MakeAppConfig(n)
	if err != nil {
		return fmt.Errorf("making app config: %w", err)
	}
	appConfigFilePath := filepath.Join(nodeDir, "config", "app.toml")
	serverconfig.WriteConfigFile(appConfigFilePath, appConfig)

	// Store the node key for the p2p handshake
	nodeKeyFilePath := filepath.Join(nodeDir, "config", "node_key.json")
	err = (&p2p.NodeKey{PrivKey: n.NetworkKey}).SaveAs(nodeKeyFilePath)
	if err != nil {
		return err
	}

	err = os.Chmod(nodeKeyFilePath, 0o777)
	if err != nil {
		return fmt.Errorf("chmod node key: %w", err)
	}

	// Store the validator signer key for consensus
	pvKeyPath := filepath.Join(nodeDir, "config", "priv_validator_key.json")
	pvStatePath := filepath.Join(nodeDir, "data", "priv_validator_state.json")
	(privval.NewFilePV(n.SignerKey, pvKeyPath, pvStatePath)).Save()

	addrBookFile := filepath.Join(nodeDir, "config", "addrbook.json")
	err = WriteAddressBook(peers, addrBookFile)
	if err != nil {
		return fmt.Errorf("writing address book: %w", err)
	}

	_, err = n.Instance.ExecuteCommand(fmt.Sprintf("mkdir -p %s/config", remoteRootDir))
	if err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}
	_, err = n.Instance.ExecuteCommand(fmt.Sprintf("mkdir -p %s/data", remoteRootDir))
	if err != nil {
		return fmt.Errorf("creating data directory: %w", err)
	}

	err = n.Instance.AddFile(configFilePath, filepath.Join(remoteRootDir, "config", "config.toml"), "10001:10001")
	if err != nil {
		return fmt.Errorf("adding config file: %w", err)
	}

	err = n.Instance.AddFile(genesisFilePath, filepath.Join(remoteRootDir, "config", "genesis.json"), "10001:10001")
	if err != nil {
		return fmt.Errorf("adding genesis file: %w", err)
	}

	err = n.Instance.AddFile(appConfigFilePath, filepath.Join(remoteRootDir, "config", "app.toml"), "10001:10001")
	if err != nil {
		return fmt.Errorf("adding app config file: %w", err)
	}

	err = n.Instance.AddFile(pvKeyPath, filepath.Join(remoteRootDir, "config", "priv_validator_key.json"), "10001:10001")
	if err != nil {
		return fmt.Errorf("adding priv_validator_key file: %w", err)
	}

	err = n.Instance.AddFile(pvStatePath, filepath.Join(remoteRootDir, "data", "priv_validator_state.json"), "10001:10001")
	if err != nil {
		return fmt.Errorf("adding priv_validator_state file: %w", err)
	}

	err = n.Instance.AddFile(nodeKeyFilePath, filepath.Join(remoteRootDir, "config", "node_key.json"), "10001:10001")
	if err != nil {
		return fmt.Errorf("adding node_key file: %w", err)
	}

	err = n.Instance.AddFile(addrBookFile, filepath.Join(remoteRootDir, "config", "addrbook.json"), "10001:10001")
	if err != nil {
		return fmt.Errorf("adding addrbook file: %w", err)
	}

	return n.Instance.Commit()
}

// AddressP2P returns a P2P endpoint address for the node. This is used for
// populating the address book. This will look something like:
// 3314051954fc072a0678ec0cbac690ad8676ab98@61.108.66.220:26656
func (n Node) AddressP2P(withID bool) string {
	ip, err := n.Instance.GetIP()
	if err != nil {
		panic(err)
	}
	addr := fmt.Sprintf("%v:%d", ip, p2pPort)
	if withID {
		addr = fmt.Sprintf("%x@%v", n.NetworkKey.PubKey().Address().Bytes(), addr)
	}
	return addr
}

// AddressRPC returns an RPC endpoint address for the node.
// This returns the local proxy port that can be used to communicate with the node
func (n Node) AddressRPC() string {
	return fmt.Sprintf("http://127.0.0.1:%d", n.rpcProxyPort)
}

// AddressGRPC returns a GRPC endpoint address for the node. This returns the
// local proxy port that can be used to communicate with the node
func (n Node) AddressGRPC() string {
	return fmt.Sprintf("127.0.0.1:%d", n.grpcProxyPort)
}

func (n Node) IsValidator() bool {
	return n.SelfDelegation != 0
}

func (n Node) Client() (*http.HTTP, error) {
	return http.New(n.AddressRPC(), "/websocket")
}

func (n *Node) Start() error {
	if err := n.Instance.Start(); err != nil {
		return err
	}

	if err := n.Instance.WaitInstanceIsRunning(); err != nil {
		return err
	}

	rpcProxyPort, err := n.Instance.PortForwardTCP(rpcPort)
	if err != nil {
		return fmt.Errorf("forwarding port %d: %w", rpcPort, err)
	}

	grpcProxyPort, err := n.Instance.PortForwardTCP(grpcPort)
	if err != nil {
		return fmt.Errorf("forwarding port %d: %w", grpcPort, err)
	}
	n.rpcProxyPort = rpcProxyPort
	n.grpcProxyPort = grpcProxyPort
	return nil
}

func (n *Node) Upgrade(version string) error {
	return n.Instance.SetImageInstant(DockerImageName(version))
}

func DockerImageName(version string) string {
	return fmt.Sprintf("%s:%s", dockerSrcURL, version)
}
