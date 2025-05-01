# Installing nocc

Here we'll install `nocc` and launch a sample cpp file to make sure everything works on a local machine.


<p><br></p>

## Installing nocc from sources

Nocc depends on the following programming/tools:
- go: To compile nocc
- mount/chroot: to provide a virtual root on a nocc-server.
At runtime nocc-daemon depends on systemd for socket creation

Clone this repo, proceed to its root, and run:

```bash
make client
make server
```

You'll have 3 binaries emitted in the `bin/` folder.

For (re)generating the generated protobuf source code, you'll also have to install protobuf compiler,

<p><br></p>

## Run a simple example locally

Install the binaries and systemd services:
```bash
make install
```

Save this fragment as `1.cpp`:

```cpp
#include "1.h"

int square(int a) { 
  return a * a; 
}
```

And this one — as `1.h`:

```cpp
int square(int a);
```

Start (as root) the Systemd services for nocc-daemon/nocc-server:
```bash
systemctl start nocc-daemon.socket
systemctl start nocc-server.service
```

Open another console: you'll run `nocc` client there. 

```bash
nocc g++ 1.cpp -o 1.o -c
```

If everything works, there should be `1.o` emitted.
To make sure that it's not just a local launch, look through server logs (journalctl) in the console (about a new client and so on).


<p><br></p>

## Configuration

Of course, launching `nocc-server` locally is useless. 
To have a performance impact, you should have multiple compilation servers with `nocc-server` running with proper options.

Proceed to the [configuration page](./configuration.md) for these details.
