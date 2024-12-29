using Go = import "/go.capnp";
@0xe6ee05a41c4c768f;
$Go.package("storage");
$Go.import("storage");

struct Group {
    name @0 :Text;
    # Name of the group.

    ownerGroup @1 :Text;
    # Name of the group owning this group.
}