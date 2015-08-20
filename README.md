gatewaysshd
===========

[![Build Status](https://travis-ci.org/ziyan/gatewaysshd.svg?branch=master)](https://travis-ci.org/ziyan/gatewaysshd)

Certificate Authority
---------------------

Generate a ssh key to be used as the certificate authority. You should set a password on this key to protect it well.

```
ssh-keygen -t rsa -b 4096 -f id_rsa.ca
```

Host Key and Certificate
------------------------

Each server that runs `gatewaysshd` needs a host key and a signed certificate. Suppose the hostname of the server is `gateway-1.example.com`. First, we generate the private key and public key pair:

```
ssh-keygen -t rsa -b 2048 -N "" -f id_rsa.gateway-1.example.com
```

Then we will need to sign the public key `id_rsa.gateway-1.example.com.pub` for this host using the certificate authority.

```
ssh-keygen -s id_rsa.ca -I gateway-1.example.com -h -n gateway-1.example.com,gateway-1 -V +52w id_rsa.gateway-1.example.com.pub
```

* `-s` specifies the path to your certificate authority
* `-I` is the key identifier, usually we can just use the fully-qualified hostname
* `-h` is **very important**, it means this certificate is for use as a host certificate on the server side
* `-n` is also **very important**, it is a comma seperated list of principals for the host certificate, meaning if the user type `ssh username@hostname`, `hostname` must be in this list of principals for the host certificate to be successfully validated
* `-V` specifies the validity period

Make sure the output of the command above says `Signed host key`. If it says `Signed user key`, then you probably forgot to supply the `-h` argument. After the command succeeds, you should get a file called `id_rsa.gateway-1.example.com-cert.pub`, that's the host certificate. There are many more options to customize your certificate, see `ssh-keygen -h`.

User Key and Certificate
------------------------

For clients that connects to an instance of `gatewaysshd`, each of them also needs a user key and a signed certificate. This is very similar to the host key and certificate. The only differences are:

* `-h` flag should be **absent** in order to specify user certificate
* `-n` is also a comma seperated list of principals, but it should be a list of usernames instead of hostnames, the user can only use one of the usernames specified in this list of principals to connect

```
ssh-keygen -t rsa -b 2048 -N "" -f id_rsa.john.doe
```

```
ssh-keygen -s id_rsa.ca -I john.doe -n john.doe,john -V +52w id_rsa.john.doe.pub
```
