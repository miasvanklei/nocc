# nocc vs distcc

<p><br></p>

## distcc architecture overview

> **Important warning!**   
> Distcc declares, that it has "pump mode", which makes it more similar to nocc. In reality, we couldn't make it work. Maybe, it really works for small and simple projects, but we had no luck with our needs. That's why here and below we speak about original distcc.

Like `nocc`, distcc is invoked with a prefix to a C++ compiler: `distcc g++ 1.cpp ...`.
Like `nocc`, distcc spreads invocations across compilation servers (nodes). The compilation is done remotely, object files are downloaded to a client machine.

But all implementation details except this basic concept are different.

<p align="center">
    <img src="img/nocc-distcc-many.drawio.png" alt="distcc many" height="451">
</p>

* **distcc invokes a C++ preprocessor locally** via `cxx -E` for every input cpp file; this makes the resulting code contain no `#include` dependencies, so distcc has to send only **one-big-all-inlined-file**; this also means that for every cpp file, the C++ compiler is launched in a preprocessor mode, which is expensive; also, distcc has to send lots of duplicated data to servers, as includes are inlined every time, even system ones
* **distcc opens a TCP connection for each invocation**, because it dies after handling an input cpp file
* **distcc does not support precompiled headers**, neither `.gch` (g++), nor `.pch` (clang)
* **distcc has no caches**, it uploads and compiles an input cpp file every time; that's why, after a project has been compiled once, compiling it again on another build agent takes the same amount of time


<p><br></p>

## nocc architecture overview

Unlike distcc, `nocc` keeps an in-memory daemon. 
And lots of other "unlike":

<p align="center">
    <img src="img/nocc-daemon.drawio.png" alt="daemon" height="356">
</p>

* **nocc gathers required includes using -M**, this way only the necessary headers/sources have to be sent
* **nocc does not send a preprocessed file**, it only sends missing sources and headers
* **nocc runs an in-memory daemon**, which keeps all connections alive, while `nocc` processes start and die; it also stores a per-build includes cache
* **nocc supports precompiled headers**, moreover, they are compiled on remotes (not locally); it works for both `.gch` and `.pch`
* **nocc has a server src cache**, so a client uploads only missing/changed cpp and headers (even if previous versions were uploaded by another client)
* **nocc has a server obj cache**, so an obj file can be sent immediately if it was once compiled with the same dependencies (even if compiled by another client)

You can read a detailed description of architecture [on a separate page](./architecture.md).


<p><br></p>

## nocc's second run is faster than the first run

When you compile a big project from scratch, it takes some time, most of the time is spent on the compilation. 

When you do it again, it's much faster. If you clean a build directory, or on another machine, or in a renamed folder — `nocc` will download already compiled obj files stored on remotes (of course, if dependent system headers and compilation flags didn't change):

<p align="center">
    <img src="img/nocc-second-run.drawio.png" alt="second run" height="255">
</p>

If compilation flags have changed so that obj files are not reusable, `nocc` will recompile all necessary cpp, but it will also be faster, as it won't need to upload files again: they were already uploaded.

What's even more interesting, a remote cache is pretty cool for switching/merging git branches. 
After merging a git branch to master, the master would also be compiled faster, because most changes were already uploaded or compiled before. 

<p align="center">
    <img src="img/nocc-after-merge.drawio.png" alt="after merge" height="325">
</p>

This works with multiple servers, as `nocc` chooses a server based on a filename.


<p><br></p>

## Timings for packages 

TODO

<p><br></p> 
