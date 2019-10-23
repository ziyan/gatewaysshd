[![Build Status](https://travis-ci.org/ziyan/gatewaysshd.svg?branch=master)](https://travis-ci.org/ziyan/gatewaysshd)

What is `gatewaysshd`?
======================

`gatewaysshd` is a daemon that provides a meeting place for all your SSH tunnels. It is especially useful if you have many hard-to-reach machines running behind firewalls and you want to access services running on them over SSH from anywhere in the world.

For example, if you have a workstation in the office, you can use `supervisor` or `upstart` or something similar to run a SSH client as a respawning daemon and make it stay always connected:

```
$ ssh -T -N workstation@gateway -R ssh:22:localhost:22
```

At home you can ssh to your workstation simply by:

```
$ ssh -T -N username@gateway -L 2222:ssh.workstation:22
```

```
$ ssh -p 2222 localhost
```

You can check what sessions are connected on the gateway server:

```
$ ssh -T username@gateway
[
  {
    "address": "1.2.3.4:1234",
    "channels_count": 1,
    "services": {},
    "timestamp": 1440668695,
    "uptime": 0,
    "user": "username"
  },
  {
    "address": "5.6.7.8:1234",
    "channels_count": 0,
    "services": {
      "ssh": [
        22
      ],
      "web": [
        80
      ]
    },
    "timestamp": 1440668286,
    "uptime": 409,
    "user": "workstation"
  }
]
```

When you remote forward a local port, `gatewaysshd` does not actually open the port on the server side. The ports you specified is a virtual concept for `gatewaysshd`. It simply keeps track of forwarded ports and internally connect and tunnel the ports when requested by another client. This relieves you the burden of assigning managing ports on the server side.

You also specifies a service name for the remote forwarded port, `ssh` or `web` for example. When connecting to these services from another client, they can be referred to as `service.username` just like a normal hostname.


Build
=====

```bash
# format code
gofmt -l -w gateway cli

# build and strip executable
CGO_ENABLED=0 go build github.com/ziyan/gatewaysshd
objcopy --strip-all gatewaysshd

# build docker image
docker build -t ziyan/gatewaysshd .
```

