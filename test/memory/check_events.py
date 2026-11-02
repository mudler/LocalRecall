import json, sys
# Infer only the exact approved top-level selectors. An explicit override must
# agree with the selector; broad/combined/subtest expressions are not gates.
selectors=[]
args=iter(sys.argv[3:])
for arg in args:
    if arg in ('-run', '--run'):
        selectors.append(next(args, ''))
    elif arg.startswith(('-run=', '--run=')):
        selectors.append(arg.split('=', 1)[1])
known={'^TestMemoryPostgres': 'TestMemoryPostgres',
       '^TestMemoryDeletePostgres': 'TestMemoryDeletePostgres'}
if len(selectors)!=1 or selectors[0] not in known:
    sys.exit('one exact approved memory -run prefix required')
prefix=known[selectors[0]]
if sys.argv[2] and sys.argv[2]!=prefix:
    sys.exit('memory test prefix disagrees with -run selector')
required={
 'TestMemoryPostgres': {
  'rag': ['ReplicaWithoutAssets'],
  'rag/engine': ['Durable','CorruptPreparing','LogicalIdentity','ReplayConflictAndReopen','TwoPoolsReservations','ConcurrentSameFence','StaleDuringEmbedding','RollbackEachChunk','CommitConnectionLoss','LegacyMigration','ConcurrentInitialization','EmbeddingFailure'],
 },
 'TestMemoryDeletePostgres': {
  'rag': ['AssetCleanupAndLegacy'],
  'rag/engine': ['ExactReplayReplica','TransactionBoundaries','WriteTakeover','PausedWriter','MaintenanceReservation'],
 },
}
if prefix not in required: sys.exit('unknown required memory test prefix')
runs=set(); passes=set(); packages=set()
for line in open(sys.argv[1]):
    try: e=json.loads(line)
    except ValueError: continue
    action=e.get('Action'); name=e.get('Test','')
    if action=='skip': sys.exit('skipped test/package is not acceptance')
    if name.startswith(prefix):
        key=(e['Package'],name)
        if action=='run': runs.add(key)
        if action=='pass': passes.add(key); packages.add(e['Package'])
if not runs or runs != passes: sys.exit('zero or incomplete memory tests')
for pkg,names in required[prefix].items():
    for suffix in names:
        key=('github.com/mudler/localrecall/'+pkg,prefix+suffix)
        if key not in passes: sys.exit('required test did not pass: '+str(key))
# Required failure/recovery subtests cannot silently disappear either.
subtests={'TestMemoryDeletePostgresTransactionBoundaries':['chunks','manifest','commit'],
          'TestMemoryPostgresRollbackEachChunk':['1','2','3','4']}
for name,children in subtests.items():
    if name.startswith(prefix):
        for child in children:
            if ('github.com/mudler/localrecall/rag/engine',name+'/'+child) not in passes:
                sys.exit('required boundary did not pass: '+name+'/'+child)
print('Executed %d real PostgreSQL tests/subtests across %d packages; no skips' % (len(passes), len(packages)))
