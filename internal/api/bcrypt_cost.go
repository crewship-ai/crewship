package api

// bcrypt_cost.go — the single place this package decides how expensive a
// password hash is.
//
// Before #2031 the number 12 was written out at six call sites (signup,
// bootstrap, password reset, profile password change, public-page token,
// and the signin timing equaliser). That made it impossible to lower for
// the test binary without lowering it for real users, so the test binary
// paid production strength — and paid it under -race, where bcrypt's
// blowfish key schedule is instrumented on every array access. That is
// what spent the `Go Race (internal/api)` job's 30-minute budget.

// ProductionBcryptCost is the work factor every password Crewship stores
// is hashed with, and the cost of the dummy hash the signin path compares
// against for unknown emails.
//
// DO NOT LOWER THIS. bcrypt's cost is a power of two — 12 is 4096 key
// expansion rounds, ~250 ms on server hardware — and that slowness is the
// entire defence for a stolen `users` table. Every step down halves an
// attacker's cost too.
//
// Lowering it is guarded by TestBcryptCost_ProductionValueIsPinned; making
// a new call site sidestep the guard by writing its own number is caught by
// TestBcryptCost_EverySiteReadsTheVar.
//
// Exported because `crewship admin reset-password` (cmd/crewship/cmd_admin.go)
// writes into the same users.hashed_password column and docs/guides/auth.mdx
// claims the cost is "held constant across signup, admin-CLI reset, and
// pairing redemption". It held by coincidence — two copies of the literal 12
// — until #2031 gave the number a name.
const ProductionBcryptCost = 12

// bcryptCost is what the handlers actually pass to bcrypt. It is a var for
// exactly one reason: the test binary lowers it, once, in TestMain
// (encryption_test_setup_test.go).
//
// No production path writes to it — TestBcryptCost_OnlyTheTestBinaryWrites
// proves that by parsing the package's non-test sources. A server therefore
// always runs at ProductionBcryptCost, whatever the test binary does to
// its own copy.
var bcryptCost = ProductionBcryptCost
