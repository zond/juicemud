using Go = import "/go.capnp";
@0xd258d93c56221e58;
$Go.package("storage");
$Go.import("storage");

struct Object {
    id @0 :Data;
    # ID of this object.

    location @1 :Data;
    # ID of the Object containing this Object.

    content @2 :List(Data);
    # ID of all Objects contained by this Object.

    subscriptions @3 :List(Text);
    # Names of all event types this Object is interested in.

    state @4 :Text;
    # The global variables of the Object as JSON.

    source @5 :Text;
    # Path to the JavaScript source controlling this Object.
}