package conf

type MasterConfig struct {
	MasterNode   MasterNode `yaml:"master"`
	Endpoints    []string   `yaml:"endpoints"`
	FencingToken uint64     `yaml:"fencing_token"`
}

type MasterNode struct {
	NodeName      string `yaml:"node_name" `
	HostName      string `yaml:"host_name" default:"localhost"`
	DataDir       string `yaml:"data_dir" default:"/tmp/leibrix/master"`
	RPCPort       int    `yaml:"rpc_port" default:"7003"`
	ListenPort    int    `yaml:"listen_port" default:"2380"`
	AdvertisePort int    `yaml:"advertise_addr" default:"2382"`
}
