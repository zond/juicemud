using Go = import "/go.capnp";
@0x92a04dbafae7b303;
$Go.package("cabinet");
$Go.import("storage/cabinet");

struct Tester {
    name @0 :Text;
    # Name of the tester.

    comment @1 :Text;
    # Comment of the tester.
}