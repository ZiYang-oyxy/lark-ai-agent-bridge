# CardKit Full-Width Implementation Plan

1. Add a focused regression test for `config.width_mode=fill` across the main
   card event families.
2. Set the root Card 2.0 width mode in `BuildLarkCard`.
3. Run formatting, targeted tests, and the full L1 Go suite.
4. Run L2 simulation and inspect representative output.
5. Rebuild the Mac Test bot from this worktree, execute a real Feishu table
   canary, cross-check audit and reply evidence, then release the Test lease.
6. Remove the completed item from `tasks.md`, review the final diff, and commit
   the verified implementation locally without pushing or publishing.
