# Booty

A simple PXE Server for booting Flatcar-Linux

```
> booty --help

Easy iPXE server for Flatcar

Usage:
  booty [flags]

Flags:
      --architecture string     Architecture to use for the iPXE server (default "amd64")
      --channel string          Flatcar channel to look for updates (default "stable")
      --dataDir string          Directory to store stateful data (default "/data")
      --debug                   Enable debug logging
  -h, --help                    help for booty
      --httpPort int            Port to use for the HTTP server (default 8080)
      --serverIP string         IP address that clients can connect to (default "127.0.0.1")
      --serverHttpPort int      Port to use for the client HTTP connection (default "80)
      --joinString string       The kubeadm join string to use to auto-join to a K8s cluster (kubeadm join 192.168.1.10:6443 --token TOKEN --discovery-token-ca-cert-hash sha256:SHA_HASH (default "")
      --updateSchedule string   Cron schedule to use for cleaning up cache files (default "* */1 * * *")
```

## Features

* PXE boot into the latest Flatcar-Linux
* MAC address based hostnames
* Automatic conversion of Container Linux Config to Ignition JSON
* JSON "Hardware Database" (right now just a MAC-to-hostname mapping)
* Automatic updates retrieved from Flatcar-Linux
* Automatic drain/reboot of nodes (in conjunction with [Kured](https://github.com/weaveworks/kured))
* Web UI to add/edit/remove hosts
* Unrecognized MAC addresses go into the brig (boot loop till the MAC is registered)


## Examples

[Example ignition config / helper scripts](examples/README.md)

### Docker

```
docker run --rm -it \
--network=host \
-v $PWD:/data/ \
ghcr.io/jeefy/booty:main \
--dataDir=/storage/ \
--joinString="kubeadm join 192.168.1.10:6443 --token ${TOKEN} --discovery-token-ca-cert-hash sha256:${SHA_HASH}
--serverIP=192.168.1.10
--serverHttpPort=8080
```

### Kubernetes

[Example deployment](examples/k8s.yaml)

This creates a configmap with the example ignition yaml config, scripts, a deployment of booty, and a service.

## Additional Thoughts

**Why?**

I like treating (most of) my machines like cattle. This is an easier and more lightweight way to tackle PXE booting and patch management.

**Can you make it do X?**

Feature requests / optimizations / PRs are welcome! Feel free to ping me [@jeefy](https://twitter.com/jeefy) on Twitter.
