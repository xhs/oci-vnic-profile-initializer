## Why

It is a shame that OCI does not automatically generate profiles for newly added VNICs.

## How

Events are fired by udev when VNICs are attached.

```
SUBSYSTEM=="net", ACTION=="add", NAME!="", RUN+="/usr/local/bin/vnic-initializer.sh %k"
# if net.ifnames=0
SUBSYSTEM=="net", ACTION=="add", NAME=="", RUN+="/usr/local/bin/vnic-initializer.sh eth%n"
```

As defined in `/etc/udev/rules.d/90-oci-vnic-config.rules`, script `vnic-initializer.sh` is called accordingly with network adapter's correct name.

```bash
#!/usr/bin/env bash

# do not block udev
/usr/bin/nohup /usr/local/bin/oci-vnic-profile-initializer $1 >> /tmp/oci-vnic.log
```

Program `oci-vnic-profile-initializer` will then generate profiles, if not exist, according to template `/etc/oci-vnic/profile.tpl`.

```go
// Available variables in template
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
```

Logs can be found in `/tmp/oci-vnic.log`.
