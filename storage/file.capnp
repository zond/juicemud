using Go = import "/go.capnp";
@0x996673971027ed3d;
$Go.package("storage");
$Go.import("storage");

struct File {
    path @0 :Text;
    # Path to the file.

    dir @1 :Bool;
    # True if the file is a directory.

    readGroup @2 :Text;
    # Name of the group allowing reads to this file.

    writeGroup @3 :Text;
    # Name of the group allowing reads and writes to this file.
}