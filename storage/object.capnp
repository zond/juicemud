using Go = import "/go.capnp";
@0xd258d93c56221e58;
$Go.package("storage");
$Go.import("storage");

struct Object {
    state @0 :Data;
    # State of the object in JSON.

    source @1 :Text;
    # Path to the file containing source for the object.
}