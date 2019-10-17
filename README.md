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


Key Management
==============

Certificate Authority
---------------------

Generate a ssh key to be used as the certificate authority. You should set a password on this key to protect it well.

```
$ ssh-keygen -t rsa -b 4096 -f id_rsa.ca
```

Host Key and Certificate
------------------------

Each server that runs `gatewaysshd` needs a host key and a signed certificate. Suppose the hostname of the server is `gateway-1.example.com`. First, we generate the private key and public key pair:

```
$ ssh-keygen -t rsa -b 2048 -N "" -f id_rsa.gateway-1.example.com
```

Then we will need to sign the public key `id_rsa.gateway-1.example.com.pub` for this host using the certificate authority. If you don't own the certificate authority, you may need to send only this `.pub` file to the certificate authority for them to sign it.

```
$ ssh-keygen -s id_rsa.ca -I gateway-1.example.com -h -n gateway-1.example.com,gateway-1 -V +52w id_rsa.gateway-1.example.com.pub
```

* `-s` specifies the path to your certificate authority
* `-I` is the key identifier, usually we can just use the fully-qualified hostname
* `-h` is **very important**, it means this certificate is for use as a host certificate on the server side
* `-n` is also **very important**, it is a comma seperated list of principals for the host certificate, meaning if the user type `ssh username@hostname`, `hostname` must be in this list of principals for the host certificate to be successfully validated
* `-V` specifies the validity period

Make sure the output of the command above says `Signed host key`. If it says `Signed user key`, then you probably forgot to supply the `-h` argument. After the command succeeds, you should get a file called `id_rsa.gateway-1.example.com-cert.pub`, that's the host certificate. There are many more options to customize your certificate, see `ssh-keygen -h`.

To run an instance of `gatewaysshd`, you will need three files:

* `id_rsa.gateway-1.example.com` is the private key, make sure no group or world access is allowed on this file
* `id_rsa.gateway-1.example.com-cert.pub` is the certificate
* `id_rsa.ca.pub` is the certificate authority's public key

User Key and Certificate
------------------------

For clients that connects to an instance of `gatewaysshd`, each of them also needs a user key and a signed certificate. This is very similar to the host key and certificate. The only differences are:

* `-h` flag should be **absent** in order to specify user certificate
* `-n` is also a comma seperated list of principals, but it should be a list of usernames instead of hostnames, the user can only use one of the usernames specified in this list of principals to connect

```
$ ssh-keygen -t rsa -b 2048 -N "" -f id_rsa.john.doe
```

```
$ ssh-keygen -s id_rsa.ca -I john.doe -n john.doe,john -V +52w id_rsa.john.doe.pub
```

The output certificate should be named `id_rsa.john.doe-cert.pub`. After the user received the signed certificate back from the authority, they should keep all three files together in their `~/.ssh` folder:

* `id_rsa.john.doe` is the private key, make sure no group or world access is allowed on this file
* `id_rsa.john.doe.pub` is the public key
* `id_rsa.john.doe-cert.pub` is the certificate

When connecting to the server, the user should use `-i ~/.ssh/id_rsa.john.doe` to specify the identity, and they also have to use an approved username in the certificate, for example `ssh -i ~/.ssh/id_rsa.john.doe john@gateway-1`.

Trusting the Certificate Authority
----------------------------------

To get rid of the `The authenticity of host can't be established` prompt for your users, you will need to add the public key of the certificate authority in the user's `~/.ssh/known_hosts` file in the following format:

```
@cert-authority *.example.com ssh-rsa AAAAB3NzaC1yc2EAAAADAQA... ca@example.com
```

* `*.example.com` is the principal that this certificate authority is valid for, obviously it needs to include all of your `gatewaysshd` servers

After that, the user will not need to confirm host identity if everything is authenticated successfully.
