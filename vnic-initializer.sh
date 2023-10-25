#!/usr/bin/env bash

# do not block udev
/usr/bin/nohup /usr/local/bin/oci-vnic-profile-initializer $1 >> /tmp/oci-vnic.log
