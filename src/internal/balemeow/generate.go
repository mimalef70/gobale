package balemeow

// Install protoc and protoc-gen-go v1.36.12 before regeneration. The minimal
// schema was manually reviewed; do not replace it with an unreviewed RPC catalog.
//go:generate protoc --go_out=. --go_opt=paths=source_relative wire/account_extended.proto wire/auxiliary.proto wire/avatar.proto wire/bale.proto wire/groups_extended.proto wire/messaging_extended.proto wire/polls_extended.proto wire/receive.proto wire/stickers_extended.proto
