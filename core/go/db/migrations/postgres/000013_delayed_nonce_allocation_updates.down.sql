BEGIN;

-- There is no ability to re-instate the signer_nonce column (and remove the pub_txn_id column) via a down migration

COMMIT;