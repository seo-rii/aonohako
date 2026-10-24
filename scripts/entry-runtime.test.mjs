import assert from 'node:assert/strict';
import test from 'node:test';
import { readFileSync, writeFileSync, mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { spawnSync } from 'node:child_process';
import { runProject, validateSubmission, SUPPORTED_BLOCKS } from './entry-runtime.mjs';

const block = (type, params = [], statements = []) => ({ type, params, statements });
const number = value => block('number', [value]);
const text = value => block('text', [value]);
const start = (...blocks) => [block('when_run_button_click', [null]), ...blocks];
function submission(scripts, mode = 'state') {
  return { format: 'entry-submission', version: 1, engineVersion: '4.0.23', graderVersion: 'entry-v1', mode,
    allowedBlocks: [...SUPPORTED_BLOCKS], project: { scenes: [{ id: 'scene' }], objects: [{ id: 'sprite',
      objectType: 'sprite', scene: 'scene', entity: { x: 0, y: 0, direction: 90, rotation: 0 }, script: scripts }],
      variables: [{ id: 'v', variableType: 'variable', value: 0 }, { id: 'l', variableType: 'list', array: [] }], messages: [{ id: 'm' }] } };
}
const scenario = overrides => JSON.stringify({ version: 1, seed: 1, ticks: 60, events: [], ...overrides });

test('pinned palette is identical to the published capability list', () => {
  assert.deepEqual(JSON.parse(readFileSync(new URL('./entry-supported-blocks.json', import.meta.url))), SUPPORTED_BLOCKS);
});

test('Entry 4.0.23 browser movement and expression oracles', () => {
  for (const [fixture, check] of [
    ['browser-project', state => { const object = Object.values(state.objects)[0]; assert.equal(object.x, 30); assert.equal(object.y, 25); assert.equal(state.variables.goldSum, 42); }],
    ['browser-expressions', state => { assert.deepEqual(Object.fromEntries(['quotient','mod','substring','char','blankZero','numericString','not','done'].map(id => [id, state.variables[id]])), {quotient:-3,mod:2,substring:'bcde',char:'한',blankZero:false,numericString:true,not:true,done:1}); }],
    ['browser-lists-rotation', state => { const object = Object.values(state.objects)[0]; assert.ok(Math.abs(object.x) < 1e-12); assert.equal(object.y,-10); assert.deepEqual(state.lists.goldList,['01','middle','02']); assert.equal(state.variables.item,'02'); assert.equal(state.variables.joined,'true!'); assert.equal(state.variables.sumBool,2); }],
  ]) {
    const envelope = submission([]);
    envelope.project = JSON.parse(readFileSync(new URL(`../internal/entrypolicy/testdata/${fixture}.json`, import.meta.url)));
    check(runProject(envelope, scenario()).state);
  }
});

test('stdio reads input lines, has empty EOF, and prints Entry dialog text', () => {
  const program = submission([start(block('ask_and_wait',[text('value?'),null]),block('dialog',[block('get_canvas_input_value'), 'speak', null]),
    block('ask_and_wait',[text('again?'),null]),block('dialog',[block('get_canvas_input_value'),'speak',null]),block('dialog',[number(1.23456),'speak',null]))], 'stdio');
  assert.equal(runProject(program,'hello\r\n').stdout, 'hello\n    \n1.235\n');
});

test('state scenarios seed randomness and dispatch keys/clicks/messages deterministically', () => {
  const program = submission([
    [block('when_some_key_pressed',[null,32]),block('move_x',[number(10),null])],
    [block('when_object_click',[null]),block('move_y',[number(20),null]),block('message_cast',['m',null])],
    [block('when_message_cast',[null,'m']),block('set_variable',['v',block('calc_rand',[null,number(1),null,number(100)])])],
  ]);
  const input = scenario({initial:{objects:{sprite:{x:2,y:3}},lists:{l:['01']}},events:[{tick:1,type:'keyDown',key:32},{tick:2,type:'click',objectId:'sprite'},{tick:3,type:'keyUp',key:32}]});
  const first=runProject(program,input),second=runProject(program,input);
  assert.deepEqual(first,second); assert.equal(first.state.objects.sprite.x,12); assert.equal(first.state.objects.sprite.y,23);
  assert.deepEqual(first.state.lists.l,['01']); assert.ok(first.state.variables.v >= 1 && first.state.variables.v <= 100);
});

test('rotation respects the exported object rotation method', () => {
  // Entry 4.0.23 Entity.setRotation forces zero for non-free rotation methods.
  for (const rotateMethod of ['free', 'none', 'vertical']) {
    for (const type of ['rotate_absolute', 'rotate_relative']) {
      const program = submission([start(block(type, [number(90), null]), block('move_direction', [number(10), null]))]);
      program.project.objects[0].rotateMethod = rotateMethod;
      const state = runProject(program, scenario({ ticks: 1 })).state.objects.sprite;
      const free = rotateMethod === 'free';
      assert.equal(state.rotation, free ? 90 : 0);
      assert.ok(Math.abs(state.x - (free ? 0 : 10)) < 1e-12);
      assert.equal(state.y, free ? -10 : 0);
    }
    if (rotateMethod !== 'free') {
      const program = submission([]);
      program.project.objects[0].rotateMethod = rotateMethod;
      program.project.objects[0].entity.rotation = 90;
      assert.equal(runProject(program, scenario()).state.objects.sprite.rotation, 0);
      assert.equal(runProject(program, scenario({ initial: { objects: { sprite: { rotation: 90 } } } })).state.objects.sprite.rotation, 0);
    }
  }
});

test('bounded state mode stops at configured ticks; stdio infinite loops fail', () => {
  const forever = submission([start(block('repeat_inf',[null],[[block('move_x',[number(1),null])]]))]);
  assert.equal(runProject(forever,scenario({ticks:5})).state.objects.sprite.x,5);
  forever.mode='stdio'; assert.throws(()=>runProject(forever,''),/Logical tick limit/);
});

test('reject unsupported blocks, invalid references, duplicate IDs, and malformed scenarios', () => {
  const base=submission([start()]);
  for (const mutate of [p=>p.project.objects.push({...p.project.objects[0]}),p=>p.project.objects[0].scene='wrong',p=>p.allowedBlocks=[],
    p=>p.project.objects[0].script[0].push(block('javascript',['process.exit()'])),p=>p.project.objects[0].script[0].push(block('set_variable',['missing',number(1)])),
    p=>p.project.objects[0].id='constructor', p=>p.project.functions=[{}]]) {
    const copy=structuredClone(base);mutate(copy);assert.throws(()=>validateSubmission(copy));
  }
  for (const input of [scenario({events:[{tick:-1,type:'keyDown',key:32}]}),scenario({ticks:36001}),scenario({events:[{tick:0,type:'click',objectId:'missing'}]}),
    scenario({initial:{objects:{sprite:{unknown:1}}}}),'{"version":1,"seed":1,"ticks":60,"events":[],"__proto__":{}}']) assert.throws(()=>runProject(base,input));
});

test('CLI validates only, rejects mode mismatch, emits fixed diagnostics and marks limits', () => {
  const dir=mkdtempSync(join(tmpdir(),'entry-runner-')); const file=join(dir,'Main.entry.json');
  try {
    const program=submission([start(block('dialog',[text('secret'), 'speak', null]))], 'stdio');
    writeFileSync(file,JSON.stringify(program));
    const run=(...args)=>spawnSync(process.execPath,[new URL('./entry-runner.mjs',import.meta.url).pathname,...args],{encoding:'utf8',input:''});
    assert.equal(run('--check',file).status,0); assert.equal(run('--check',file).stdout,'');
    const mismatch=run(file,'state');assert.equal(mismatch.status,1);assert.equal(mismatch.stdout,'');assert.equal(mismatch.stderr,'Entry execution failed\n');
    program.project.objects[0].script=[start(block('repeat_inf',[null],[[]]))];writeFileSync(file,JSON.stringify(program));
    assert.equal(run(file,'stdio').status,124);
  } finally { rmSync(dir,{recursive:true,force:true}); }
});
