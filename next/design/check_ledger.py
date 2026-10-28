#!/usr/bin/env python3
"""Validate migration enumeration; completion is a separate fail-closed query."""
import argparse, collections, json, pathlib, sys
HERE=pathlib.Path(__file__).resolve().parent

def readiness(ledger, inventory):
    pending=[r['id'] for r in ledger['rules'] if r['decisionStatus']=='unresolved' or r['implementationStatus']!='verified']
    pending += [r['path'] for r in inventory['mutatingCommands'] if r['decisionStatus']=='unresolved' or r['implementationStatus']!='verified']
    if pending and ledger['coverage']['allMistakesCovered']:
        raise ValueError('allMistakesCovered contradicts unresolved or unverified rows')
    return pending

def validate(ledger, inventory):
    assert ledger['schemaVersion']==inventory['schemaVersion']==1
    assert ledger['sourceRevision']==inventory['sourceRevision']
    rules=ledger['rules']; commands=inventory['mutatingCommands']; coverage=ledger['coverage']
    assert len(rules)==len({r['id'] for r in rules})==coverage['sourceRuleCount']==coverage['coveredRuleCount']==80
    assert len(commands)==len({r['path'] for r in commands})==coverage['mutatingCommandCount']==coverage['coveredMutatingCommandCount']==156
    assert sum(r['factoryExpansion'] for r in commands)==8
    for r in rules+commands:
        assert r['disposition'] in ['preserve','redesign','retire','requires-human-decision']
        assert r['verificationOwner'] and r['check'] and r['source']['path'] and r['source']['startLine']>0
        assert r['decisionStatus']==('unresolved' if r['disposition']=='requires-human-decision' else 'proposed')
    unresolved=sum(r['decisionStatus']=='unresolved' for r in rules+commands)
    assert unresolved==coverage['unresolvedCount']==len(coverage['unresolved'])
    assert sum(bool(r['trustRows']) for r in commands)==coverage['documentedTableRowCount']==84
    assert all(r['trustSources'] for r in commands)
    return readiness(ledger,inventory)

p=argparse.ArgumentParser()
p.add_argument('--require-covered',action='store_true',help='Refuse3 while any decision or implementation is unresolved/unverified.')
p.add_argument('--self-check',action='store_true',help='Reject a forged all-covered claim in memory; no source mutation.')
a=p.parse_args()
ledger=json.loads((HERE/'invariants.yaml').read_text()); inventory=json.loads((HERE/'command-inventory.json').read_text())
pending=validate(ledger,inventory)
if a.self_check:
    forged=json.loads(json.dumps(ledger));forged['coverage']['allMistakesCovered']=True
    try:validate(forged,inventory)
    except ValueError as e: print('PASS causal negative: forged allMistakesCovered rejected: '+str(e))
    else:raise AssertionError('forged readiness was accepted')
print(json.dumps({'enumeration':'valid','rules':80,'mutatingCommands':156,'unresolvedDecisions':ledger['coverage']['unresolvedCount'],'pendingRows':len(pending),'allMistakesCovered':False}))
if a.require_covered:
    if pending:
        print('REFUSED: unresolved or unverified ledger rows prevent all-covered claim',file=sys.stderr)
        sys.exit(3)
