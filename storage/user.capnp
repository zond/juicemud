using Go = import "/go.capnp";
@0xa687d0c53195176c;
$Go.package("storage");
$Go.import("storage");

struct User {
    name @0 :Text;
    # Name of the user.

    passwordHash @1 :Data;
    # Hash of the password of the user.

    owner @2 :Bool;
    # True if the user is a system owner.
}