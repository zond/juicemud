using Go = import "/go.capnp";
@0xcba563517c832338;
$Go.package("storage");
$Go.import("storage");

struct GroupMember {
    user @0 :Text;
    # Name of the user.

    group @1 :Text;
    # Name of the group.
}