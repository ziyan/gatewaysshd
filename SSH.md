SSH Key Management
==================

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

