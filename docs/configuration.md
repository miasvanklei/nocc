# nocc configuration

This page describes how to set up `nocc-daemon` and `nocc-server` for production.


<p><br></p>

## Configuring nocc-daemon

All configuration on a server-side is done using a configuration file, located at /etc/nocc/daemon.conf. For an example see 'data/nocc-daemon.conf.example'

|  Configuration setting           | Description                                                                                                                                                                              |
|----------------------------------|------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `ClientId          = {string}`   | This is a *clientID* sent to all servers when a daemon starts. Setting a sensible value makes server logs much more readable. If not set, a random string is generated on daemon start.  |
| `SocksProxyAddr    = {string}`   | Let nocc-daemon communicate through a socks5 proxy                                                                                                                                       |
| `CompilerQueueSize = {string}`   | Amount of parallel processes when remotes aren't available and compiler is launched locally. By default, it's the number of CPUs on the current machine.                                 |
| `Servers           = []{string}` | Remote nocc servers — an array of 'host:port'.                                                                                                                                           |
| `LogFileName       = {string}`   | A filename to log, nothing by default. Errors are duplicated to stderr always.always.                                                                                                    |
| `LogLevel          = {int}`      | Logger verbosity level for INFO (-1 off, default 0, max 2). Errors are always logged                                                                                                     |

For real usage, you'll definitely have to specify `Servers`. It also makes sense of setting `ClientId` Other options are unlikely to be used. 

When you launch lots of jobs like `make -j 600`, then `nocc-daemon` has to maintain lots of local connections and files at the same time. If you face a "too many open files" error, consider increasing `ulimit -n`.


<p><br></p>

## Configuring nocc server

All configuration on a server-side is done using a configuration file, located at /etc/nocc/server.conf. For an example see 'data/nocc-server.conf.example'

| Configuration setting          | Description                                                                             |
|--------------------------------|-----------------------------------------------------------------------------------------|
| `ListenAddr        = {string}` | Binding address, default localhost:43210                                                |
| `SrcCacheDir       = {string}` | Directory for incoming source/header files, default */tmp/nocc/cpp*.                    |
| `ObjCacheDir       = {string}` | Directory for resulting obj files and obj cache, default */tmp/nocc/obj*.               |
| `LogFilename       = {string}` | A filename to log, by default use stderr.                                               |
| `LogLevel          = {int}`    | Logger verbosity level for INFO (-1 off, default 0, max 2). Errors are logged always.   |
| `SrcCacheSize      = {int}`    | Header and source cache limit, in bytes, default 4G.                                    |
| `ObjCacheSize      = {int}`    | Compiled obj cache limit, in bytes, default 16G.                                        |
| `CompilerQueueSize = {int}`    | Max amount of C++ compiler processes launched in parallel, default *nCPU*.              |

All file caches are lost on restart, as references to files are kept in memory. 
There is also an LRU expiration mechanism to fit cache limits.

When `nocc-server` restarts, it ensures that *working-dir* is empty. 
If not, it's renamed to *working-dir.old*. 
If *working-dir.old* already exists, it's removed recursively.
That's why restarting can take a noticable time if there were lots of files saved in working dir by a previous run.


<p><br></p>

## Server log rotation

When a `nocc-server` process receives the `SIGUSR1` signal, it reopens the specified `LogFilename` again.


<p><br></p>

## Configuring nocc + tmpfs

The directory passed as `SrcCacheDir` can be placed in **tmpfs**. 
All operations with cpp files are performed in that directory: 
* incoming files (h/cpp/etc.) are saved there mirroring client's file structure;
* src-cache is placed there;
* pch files are placed there;
* tmp files for preventing race conditions are also there, not in sys tmp dir.

So, if that directory is placed in tmpfs, the C++ compiler will take all files from memory (except for system headers),
which noticeably speeds up compilation.

When setting up limits to tmpfs in a system, ensure that it will fit `SrcCacheSize` plus some extra space.

Note, that placing `ObjCacheDir` in tmpfs is not recommended, because obj files are usually much heavier,
and they are just transparently streamed back from a hard disk in chunks.


<p><br></p>

## Other commands

`nocc-daemon/nocc-server` has some commands aside from configuration:

* `nocc -version` / `nocc -v` — show version and exit

