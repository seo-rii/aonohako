// entry-v1: a deterministic, deliberately bounded subset of Entry 4.0.23.
// This module has no ambient I/O and is also used for browser previews.
export const ENGINE_VERSION = '4.0.23';
export const GRADER_VERSION = 'entry-v1';
export const LIMITS = Object.freeze({ projectBytes: 524288, submissionBytes: 614400,
  inputBytes: 131072, depth: 64, nodes: 30000, blocks: 10000, objects: 100,
  variables: 1000, listItems: 10000, stringLength: 65536, ticks: 36000,
  events: 1000, threads: 256, steps: 1000000, outputBytes: 1048576 });

export const SUPPORTED_BLOCKS = Object.freeze([
  'False', 'True', '_if', 'add_value_to_list', 'angle', 'ask_and_wait',
  'boolean_and_or', 'boolean_basic_operator', 'boolean_not', 'boolean_shell',
  'calc_basic', 'calc_rand', 'change_scale_size', 'change_value_list_index',
  'change_variable', 'char_at', 'combine_something', 'continue_repeat',
  'coordinate_object', 'dialog', 'direction_absolute', 'direction_relative',
  'get_boolean_value', 'get_canvas_input_value', 'get_variable', 'hide', 'if_else',
  'insert_value_to_list', 'is_included_in_list', 'is_press_some_key',
  'length_of_list', 'length_of_string', 'locate_x', 'locate_xy', 'locate_y',
  'message_cast', 'move_direction', 'move_x', 'move_y', 'number',
  'quotient_and_mod', 'remove_dialog', 'remove_value_from_list', 'repeat_basic',
  'repeat_inf', 'repeat_while_true', 'rotate_absolute', 'rotate_relative',
  'set_scale_size', 'set_variable', 'show', 'stop_object', 'stop_repeat',
  'substring', 'text', 'true_or_false', 'value_of_index_from_list', 'wait_second',
  'wait_until_true', 'when_message_cast', 'when_object_click',
  'when_run_button_click', 'when_some_key_pressed',
]);
const supported = new Set(SUPPORTED_BLOCKS);
const hats = new Set(['when_run_button_click', 'when_some_key_pressed', 'when_object_click', 'when_message_cast']);
const expressions = new Set(['False', 'True', 'angle', 'boolean_and_or',
  'boolean_basic_operator', 'boolean_not', 'boolean_shell', 'calc_basic', 'calc_rand',
  'char_at', 'combine_something', 'coordinate_object', 'get_boolean_value',
  'get_canvas_input_value', 'get_variable', 'is_included_in_list', 'is_press_some_key',
  'length_of_list', 'length_of_string', 'number', 'quotient_and_mod', 'substring',
  'text', 'true_or_false', 'value_of_index_from_list']);
const statementCounts = { repeat_basic: 1, repeat_inf: 1, repeat_while_true: 1, _if: 1, if_else: 2 };
const numericState = new Set(['x', 'y', 'direction', 'rotation', 'size']);
const forbiddenKeys = new Set(['__proto__', 'prototype', 'constructor']);
const encoder = new TextEncoder();
const bytes = value => encoder.encode(value).length;
const record = value => value !== null && typeof value === 'object' && !Array.isArray(value);
const own = (value, key) => Object.prototype.hasOwnProperty.call(value, key);
const dict = () => Object.create(null);
const fail = message => { throw new Error(message); };
const requireThat = (condition, message) => { if (!condition) fail(message); };
const finite = value => typeof value === 'number' && Number.isFinite(value) && Math.abs(value) <= 1e15;
const scalar = value => typeof value === 'boolean' || finite(value) ||
  (typeof value === 'string' && value.length <= LIMITS.stringLength);
const identifier = value => typeof value === 'string' && value.length > 0 && value.length <= 128 && !forbiddenKeys.has(value);
const numeric = value => typeof value === 'number' ||
  (typeof value === 'string' && /^-?\d+\.?\d*(?:[eE][+-]?\d+)?$/.test(value));
// Entry.Script.getNumberValue uses parseFloat(value) || 0.
const num = value => { const n = parseFloat(value) || 0; requireThat(finite(n), 'Number is outside the supported range'); return n; };
const text = value => value === true ? 'True' : value === false ? 'False' : String(value);
const cleanNumber = value => { requireThat(finite(value), 'Arithmetic result is outside the supported range'); return Number(value.toPrecision(15)); };
const normalizedScalar = value => { requireThat(scalar(value), 'Invalid scalar value'); return value; };
const boolean = value => value === undefined ? false : Number.isNaN(Number(value)) || Boolean(Number(value));
const strictKeys = (value, keys, label) => {
  requireThat(record(value) && Object.keys(value).every(key => keys.includes(key)), `Invalid ${label}`);
};

function boundedTree(value) {
  const stack = [[value, 0]];
  let count = 0;
  while (stack.length) {
    const [item, depth] = stack.pop();
    requireThat(++count <= LIMITS.nodes && depth <= LIMITS.depth, 'JSON structure exceeds limits');
    if (typeof item === 'number') requireThat(Number.isFinite(item), 'Nonfinite JSON number');
    if (item && typeof item === 'object') {
      for (const [key, child] of Object.entries(item)) {
        requireThat(!forbiddenKeys.has(key), 'Unsafe JSON property');
        stack.push([child, depth + 1]);
      }
    }
  }
}

/** Validate an immutable submission and return a private normalized execution plan. */
export function validateSubmission(submission) {
  if (typeof submission === 'string') {
    requireThat(bytes(submission) <= LIMITS.submissionBytes, 'Submission is too large');
    try { submission = JSON.parse(submission); } catch { fail('Invalid submission JSON'); }
  }
  boundedTree(submission);
  strictKeys(submission, ['format', 'version', 'engineVersion', 'graderVersion', 'mode', 'allowedBlocks', 'project'], 'submission envelope');
  requireThat(submission.format === 'entry-submission' && submission.version === 1 &&
    submission.engineVersion === ENGINE_VERSION && submission.graderVersion === GRADER_VERSION,
    'Unsupported Entry submission version');
  requireThat(submission.mode === 'stdio' || submission.mode === 'state', 'Invalid judging mode');
  const project = submission.project;
  requireThat(record(project) && bytes(JSON.stringify(project)) <= LIMITS.projectBytes, 'Invalid or oversized Entry project');
  let allowed = supported;
  if (submission.allowedBlocks !== undefined) {
    const list = submission.allowedBlocks;
    requireThat(Array.isArray(list) && list.length > 0 && list.length <= supported.size &&
      new Set(list).size === list.length && list.every(type => supported.has(type)), 'Invalid block allowlist');
    allowed = new Set(list);
  }
  for (const key of ['functions', 'tables', 'extensions', 'expansionBlocks', 'aiUtilizeBlocks',
    'hardwareLiteBlocks', 'externalModules', 'externalModulesLite']) {
    requireThat(project[key] === undefined || (Array.isArray(project[key]) && project[key].length === 0), 'Unsupported project extension');
  }
  requireThat(Array.isArray(project.scenes) && project.scenes.length === 1 && identifier(project.scenes[0]?.id), 'The judge supports one scene');
  requireThat(Array.isArray(project.objects) && project.objects.length > 0 && project.objects.length <= LIMITS.objects, 'Invalid object count');
  const objects = new Map();
  for (const object of project.objects) {
    requireThat(record(object) && identifier(object.id) && !objects.has(object.id), 'Invalid or duplicate object ID');
    requireThat(object.objectType === 'sprite' && object.scene === project.scenes[0].id, 'Unsupported object or scene');
    requireThat(object.rotateMethod === undefined || ['free', 'vertical', 'none'].includes(object.rotateMethod), 'Unsupported rotation method');
    requireThat(record(object.entity), 'Missing object entity');
    const entity = object.entity;
    const state = { x: entity.x ?? 0, y: entity.y ?? 0, direction: entity.direction ?? 90,
      rotation: entity.rotation ?? 0, visible: entity.visible ?? true,
      size: Math.abs(entity.scaleX ?? 1) * 100, say: '' };
    requireThat([...numericState].every(key => finite(state[key])) && typeof state.visible === 'boolean', 'Invalid object state');
    if ((object.rotateMethod ?? 'free') !== 'free') state.rotation = 0;
    let scripts = object.script;
    if (typeof scripts === 'string') { try { scripts = JSON.parse(scripts); } catch { fail('Invalid object script JSON'); } }
    requireThat(Array.isArray(scripts), 'Invalid object script');
    boundedTree(scripts);
    objects.set(object.id, { object, state, scripts });
  }
  const variables = new Map();
  const lists = new Map();
  requireThat(Array.isArray(project.variables ?? []) && (project.variables ?? []).length <= LIMITS.variables, 'Invalid variables');
  let listItems = 0;
  for (const variable of project.variables ?? []) {
    requireThat(record(variable) && identifier(variable.id) && !variables.has(variable.id) && !lists.has(variable.id), 'Invalid or duplicate variable ID');
    requireThat(!variable.isCloud && !variable.cloud && !variable.isRealTime, 'Shared variables are unsupported');
    requireThat(variable.object == null || objects.has(variable.object), 'Invalid variable owner');
    const type = variable.variableType ?? 'variable';
    requireThat(['variable', 'list', 'answer', 'timer'].includes(type), 'Unsupported variable type');
    if (type === 'list') {
      requireThat(Array.isArray(variable.array), 'Invalid list');
      const values = variable.array.map(item => { requireThat(record(item) && scalar(item.data), 'Invalid list item'); return normalizedScalar(item.data); });
      listItems += values.length;
      lists.set(variable.id, { owner: variable.object, values });
    } else {
      requireThat(scalar(variable.value), 'Invalid variable value');
      variables.set(variable.id, { owner: variable.object, type,
        value: type === 'timer' ? 0 : type === 'answer' ? '' : normalizedScalar(variable.value) });
    }
  }
  requireThat(listItems <= LIMITS.listItems, 'Too many list items');
  requireThat(Array.isArray(project.messages ?? []) && (project.messages ?? []).length <= LIMITS.variables, 'Invalid messages');
  const messages = new Set();
  for (const message of project.messages ?? []) {
    requireThat(record(message) && identifier(message.id) && !messages.has(message.id), 'Invalid message ID');
    messages.add(message.id);
  }
  let blockCount = 0;
  const checkRef = (map, id, objectId) => requireThat(map.has(id) &&
    (map.get(id).owner == null || map.get(id).owner === objectId), 'Invalid variable or list reference');
  const checkBlock = (block, objectId, kind, depth) => {
    requireThat(depth <= LIMITS.depth && ++blockCount <= LIMITS.blocks, 'Too many blocks or excessive nesting');
    requireThat(record(block) && allowed.has(block.type), 'Unsupported or disallowed block');
    requireThat(kind === 'expression' ? expressions.has(block.type) : !expressions.has(block.type), 'Invalid block position');
    requireThat(Array.isArray(block.params) && block.params.length <= 10 && Array.isArray(block.statements ?? []), 'Invalid block parameters');
    const statements = block.statements ?? [];
    requireThat(statements.length === (statementCounts[block.type] ?? 0), 'Invalid block statements');
    requireThat(block.extensions === undefined || (Array.isArray(block.extensions) && block.extensions.length === 0), 'Unsupported block extension');
    for (const param of block.params) {
      if (record(param)) checkBlock(param, objectId, 'expression', depth + 1);
      else requireThat(param === null || scalar(param), 'Invalid block parameter');
    }
    for (const statement of statements) {
      requireThat(Array.isArray(statement), 'Invalid statement');
      for (const child of statement) { requireThat(!hats.has(child?.type), 'Nested event block'); checkBlock(child, objectId, 'statement', depth + 1); }
    }
    const p = block.params;
    if (['get_variable', 'set_variable', 'change_variable'].includes(block.type)) checkRef(variables, p[0], objectId);
    if (['value_of_index_from_list', 'length_of_list', 'is_included_in_list', 'add_value_to_list', 'remove_value_from_list', 'insert_value_to_list'].includes(block.type)) checkRef(lists, p[1], objectId);
    if (block.type === 'change_value_list_index') checkRef(lists, p[0], objectId);
    if (block.type === 'coordinate_object') requireThat((p[1] === 'self' || objects.has(p[1])) &&
      ['x', 'y', 'rotation', 'direction', 'size', 'picture_index', 'picture_name'].includes(p[3]), 'Invalid coordinate reference');
    if (block.type === 'message_cast') requireThat(messages.has(p[0]), 'Invalid message reference');
    if (block.type === 'when_message_cast') requireThat(messages.has(p[1]), 'Invalid message reference');
  };
  for (const [id, object] of objects) {
    requireThat(object.scripts.length <= LIMITS.threads, 'Too many scripts');
    for (const script of object.scripts) {
      requireThat(Array.isArray(script) && script.length > 0 && hats.has(script[0]?.type), 'Every script must start with a supported event');
      script.forEach((block, index) => { requireThat(index === 0 || !hats.has(block?.type), 'Misplaced event block'); checkBlock(block, id, 'statement', 0); });
    }
  }
  return { mode: submission.mode, objects, variables, lists, messages };
}

function scenarioFromInput(mode, input) {
  requireThat(typeof input === 'string' && bytes(input) <= LIMITS.inputBytes, 'Input exceeds limits');
  if (mode === 'stdio') return { version: 1, seed: 1, ticks: LIMITS.ticks, events: [], input };
  let scenario;
  try { scenario = JSON.parse(input); } catch { fail('Invalid state scenario'); }
  boundedTree(scenario);
  strictKeys(scenario, ['version', 'seed', 'ticks', 'events', 'initial', 'input'], 'state scenario');
  requireThat(scenario.version === 1 && Number.isInteger(scenario.seed) && scenario.seed >= 0 && scenario.seed <= 0xffffffff &&
    Number.isInteger(scenario.ticks) && scenario.ticks >= 1 && scenario.ticks <= LIMITS.ticks &&
    Array.isArray(scenario.events) && scenario.events.length <= LIMITS.events &&
    (scenario.input === undefined || typeof scenario.input === 'string'), 'Invalid state scenario');
  let previous = -1;
  for (const event of scenario.events) {
    strictKeys(event, event?.type === 'click' ? ['tick', 'type', 'objectId'] : ['tick', 'type', 'key'], 'state event');
    requireThat(Number.isInteger(event.tick) && event.tick >= 0 && event.tick >= previous && event.tick < scenario.ticks &&
      ['keyDown', 'keyUp', 'click'].includes(event.type), 'Invalid state event');
    if (event.type === 'click') requireThat(identifier(event.objectId), 'Invalid click target');
    else requireThat(Number.isInteger(event.key) && event.key >= 0 && event.key <= 255, 'Invalid key code');
    previous = event.tick;
  }
  return scenario;
}

/** Execute without I/O, wall clocks, eval, promises, or contestant JavaScript. */
export function runProject(submission, input = '') {
  const plan = validateSubmission(submission);
  const scenario = scenarioFromInput(plan.mode, input);
  const { objects, variables, lists } = plan;
  if (scenario.initial !== undefined) {
    strictKeys(scenario.initial, ['objects', 'variables', 'lists'], 'initial state');
    for (const [kind, values] of Object.entries(scenario.initial)) {
      requireThat(record(values), 'Invalid initial state');
      for (const [id, value] of Object.entries(values)) {
        if (kind === 'objects') {
          requireThat(objects.has(id), 'Unknown initial object');
          strictKeys(value, [...numericState, 'visible'], 'initial object');
          for (const [key, field] of Object.entries(value)) requireThat(key === 'visible' ? typeof field === 'boolean' : finite(field), 'Invalid initial object value');
          Object.assign(objects.get(id).state, value);
          if ((objects.get(id).object.rotateMethod ?? 'free') !== 'free') objects.get(id).state.rotation = 0;
        } else if (kind === 'variables') {
          requireThat(variables.has(id) && variables.get(id).type === 'variable' && scalar(value), 'Unknown or invalid initial variable');
          variables.get(id).value = normalizedScalar(value);
        } else {
          requireThat(lists.has(id) && Array.isArray(value) && value.every(scalar), 'Unknown or invalid initial list');
          lists.get(id).values = value.map(normalizedScalar);
        }
      }
    }
  }
  for (const event of scenario.events) if (event.type === 'click') requireThat(objects.has(event.objectId), 'Unknown click target');
  let listCount = [...lists.values()].reduce((count, list) => count + list.values.length, 0);
  requireThat(listCount <= LIMITS.listItems, 'Too many list items');
  const lines = (scenario.input ?? '').replace(/\r\n/g, '\n').split('\n');
  if (lines.at(-1) === '') lines.pop();
  let lineIndex = 0, answer = '', steps = 0, tick = 0, stdout = '', outputSize = 0;
  let rng = scenario.seed >>> 0;
  // Mulberry32 has a defined uint32 seed including zero.
  const random = () => { rng = (rng + 0x6d2b79f5) >>> 0; let t = rng; t = Math.imul(t ^ t >>> 15, t | 1); t ^= t + Math.imul(t ^ t >>> 7, t | 61); return ((t ^ t >>> 14) >>> 0) / 4294967296; };
  const charge = () => requireThat(++steps <= LIMITS.steps, 'Instruction limit exceeded');
  const keys = new Set();
  const threads = [];
  let pending = [];
  const BREAK = Symbol('break'), CONTINUE = Symbol('continue'), STOP = Symbol('stop');
  const normalizeAngle = value => (value % 360 + 360) % 360;
  const getList = id => { requireThat(lists.has(id), 'Unknown list'); return lists.get(id).values; };
  const listIndex = (value, list, insert = false) => {
    if (value === 'FIRST') value = 1;
    else if (value === 'LAST') value = list.length;
    else if (value === 'RANDOM') value = Math.floor(random() * list.length) + 1;
    const index = num(value);
    requireThat(Number.isInteger(index) && index >= 1 && index <= list.length + (insert ? 1 : 0), 'List index is out of range');
    return index - 1;
  };
  const evaluate = (value, objectId) => {
    if (!record(value)) return value;
    charge();
    const p = value.params, at = index => evaluate(p[index], objectId), n = index => num(at(index)), s = index => String(at(index));
    let result;
    switch (value.type) {
      case 'number': case 'angle': result = num(p[0]); break;
      case 'text': result = p[0] ?? ''; break;
      case 'True': result = true; break;
      case 'False': case 'boolean_shell': result = false; break;
      case 'true_or_false': result = p[0] === 'true' || p[0] === true; break;
      case 'get_boolean_value': result = at(0) ? 'TRUE' : 'FALSE'; break;
      case 'get_canvas_input_value': result = answer; break;
      case 'get_variable': result = variables.get(p[0]).value; break;
      case 'is_press_some_key': result = keys.has(Number(p[0])); break;
      case 'boolean_not': result = !boolean(at(1)); break;
      case 'boolean_and_or': { const a = Boolean(at(0)), b = Boolean(at(2)); requireThat(['AND', 'OR'].includes(p[1]), 'Unknown Boolean operator'); result = p[1] === 'AND' ? a && b : a || b; break; }
      case 'boolean_basic_operator': {
        let a = at(0), b = at(2);
        if (typeof a === 'string' && a.length && Number.isFinite(Number(a))) a = Number(a);
        if (typeof b === 'string' && b.length && Number.isFinite(Number(b))) b = Number(b);
        switch (p[1]) {
          case 'EQUAL': result = a === b; break;
          case 'NOT_EQUAL': result = a != b; break;
          case 'GREATER': result = a > b; break;
          case 'LESS': result = a < b; break;
          case 'GREATER_OR_EQUAL': result = a >= b; break;
          case 'LESS_OR_EQUAL': result = a <= b; break;
          default: fail('Unknown comparison operator');
        } break;
      }
      case 'calc_basic': {
        const a = at(0), b = at(2);
        switch (p[1]) {
          case 'PLUS': { const left = numeric(a) ? num(a) : a, right = numeric(b) ? num(b) : b; result = left + right; if (typeof result === 'number') result = cleanNumber(result); break; }
          case 'MINUS': result = cleanNumber(num(a) - num(b)); break;
          case 'MULTI': result = cleanNumber(num(a) * num(b)); break;
          case 'DIVIDE': result = cleanNumber(num(a) / num(b)); break;
          default: fail('Unknown arithmetic operator');
        } break;
      }
      case 'quotient_and_mod': { const a = n(1), b = n(3); requireThat(b !== 0 && ['QUOTIENT', 'MOD'].includes(p[5]), 'Invalid quotient or remainder'); result = p[5] === 'QUOTIENT' ? Math.floor(a / b) : cleanNumber(a - b * Math.floor(a / b)); break; }
      case 'calc_rand': { const a = n(1), b = n(3), low = Math.min(a, b), high = Math.max(a, b); result = Number.isInteger(a) && Number.isInteger(b) ? Math.floor(random() * (high - low + 1)) + low : (random() * (high - low) + low).toFixed(2); break; }
      case 'combine_something': result = s(1) + s(3); break;
      case 'length_of_string': result = s(1).length; break;
      case 'char_at': { const str = s(1), index = n(3) - 1; requireThat(Number.isInteger(index) && index >= 0 && index < str.length, 'Character index is out of range'); result = str[index]; break; }
      case 'substring': { const str = s(1), start = n(3) - 1, end = n(5) - 1; requireThat(Number.isInteger(start) && Number.isInteger(end) && Math.min(start, end) >= 0 && Math.max(start, end) < str.length, 'Substring index is out of range'); result = str.substring(Math.min(start, end), Math.max(start, end) + 1); break; }
      case 'length_of_list': result = getList(p[1]).length; break;
      case 'value_of_index_from_list': { const list = getList(p[1]); result = list[listIndex(at(3), list)]; break; }
      case 'is_included_in_list': { const val = s(3); result = getList(p[1]).some(item => String(item) === val); break; }
      case 'coordinate_object': {
        const object = objects.get(p[1] === 'self' ? objectId : p[1]);
        if (p[3] === 'picture_index' || p[3] === 'picture_name') {
          const pictures = object.object.sprite?.pictures ?? [];
          const index = pictures.findIndex(picture => picture.id === object.object.selectedPictureId);
          result = p[3] === 'picture_index' ? index + 1 : pictures[index]?.name ?? '';
        } else result = p[3] === 'size' ? Number(object.state.size.toFixed(1)) : object.state[p[3]];
        break;
      }
      default: fail('Unsupported expression');
    }
    requireThat(scalar(result), 'Expression result exceeds limits');
    return result;
  };
  const emit = value => {
    const line = text(value) + '\n'; outputSize += bytes(line);
    requireThat(outputSize <= LIMITS.outputBytes, 'Output limit exceeded'); stdout += line;
  };
  function* runSequence(blocks, thread) {
    for (const block of blocks) {
      if (!thread.alive) throw STOP;
      charge();
      const p = block.params, at = index => evaluate(p[index], thread.objectId), n = index => num(at(index));
      const object = objects.get(thread.objectId), state = object.state;
      const body = index => runSequence(block.statements[index], thread);
      switch (block.type) {
        case 'repeat_basic': case 'repeat_inf': case 'repeat_while_true': {
          let remaining = block.type === 'repeat_basic' ? Math.floor(n(0)) : Infinity;
          requireThat(remaining >= 0, 'Negative repeat count');
          if (block.type === 'repeat_while_true') requireThat(['until', 'while'].includes(p[1]), 'Unknown repeat condition');
          while (remaining-- > 0) {
            charge();
            if (block.type === 'repeat_while_true' && (p[1] === 'until' ? boolean(at(0)) : !boolean(at(0)))) break;
            try { yield* body(0); } catch (error) { if (error === BREAK) break; if (error !== CONTINUE) throw error; }
            yield 1;
          } break;
        }
        case '_if': if (boolean(at(0))) yield* body(0); break;
        case 'if_else': yield* body(boolean(at(0)) ? 0 : 1); break;
        case 'wait_second': { const seconds = n(0); requireThat(seconds >= 0 && seconds <= LIMITS.ticks / 60, 'Wait exceeds limits'); yield Math.max(1, Math.ceil(seconds * 60)); break; }
        case 'wait_until_true': while (!boolean(at(0))) { charge(); yield 1; } break;
        case 'stop_repeat': throw BREAK;
        case 'continue_repeat': throw CONTINUE;
        case 'stop_object': {
          requireThat(['all', 'thisOnly', 'thisObject', 'thisThread', 'otherThread', 'other_objects'].includes(p[0]), 'Unknown stop target');
          for (const other of threads) {
            if (p[0] === 'all' || (['thisOnly', 'thisObject'].includes(p[0]) && other.objectId === thread.objectId) ||
              (p[0] === 'thisThread' && other === thread) || (p[0] === 'otherThread' && other !== thread && other.objectId === thread.objectId) ||
              (p[0] === 'other_objects' && other.objectId !== thread.objectId)) other.alive = false;
          }
          if (p[0] === 'all') pending = [];
          if (!thread.alive) throw STOP;
          break;
        }
        case 'set_variable': variables.get(p[0]).value = normalizedScalar(at(1)); break;
        case 'change_variable': { const variable = variables.get(p[0]), value = at(1); requireThat(value !== false, 'Cannot increment by false'); if (numeric(variable.value) && numeric(value)) { const decimals = Math.min(20, ...[variable.value, value].map(item => String(item).includes('.') ? String(item).split('.')[1].length : 0).sort((a, b) => b - a).slice(0, 1)); variable.value = cleanNumber(num(variable.value) + num(value)).toFixed(decimals); } else variable.value = String(variable.value) + String(value); requireThat(scalar(variable.value), 'Variable value exceeds limits'); break; }
        case 'add_value_to_list': { requireThat(++listCount <= LIMITS.listItems, 'List item limit exceeded'); getList(p[1]).push(normalizedScalar(at(0))); break; }
        case 'remove_value_from_list': { const list = getList(p[1]); list.splice(listIndex(at(0), list), 1); --listCount; break; }
        case 'insert_value_to_list': { const list = getList(p[1]), value = normalizedScalar(at(0)), index = listIndex(at(2), list, true); requireThat(++listCount <= LIMITS.listItems, 'List item limit exceeded'); list.splice(index, 0, value); break; }
        case 'change_value_list_index': { const list = getList(p[0]); list[listIndex(at(1), list)] = normalizedScalar(at(2)); break; }
        case 'ask_and_wait': requireThat(at(0) !== '', 'Question must not be empty'); answer = lines[lineIndex++] ?? ''; for (const variable of variables.values()) if (variable.type === 'answer') variable.value = answer; state.say = ''; yield 1; break;
        case 'dialog': { const value = at(0), message = value === '' ? '    ' : text(value); state.say = numeric(message) && !Number.isInteger(Number(message)) ? String(Number(Math.round(Number(message) * 1000) / 1000)) : message; if (plan.mode === 'stdio') emit(state.say); break; }
        case 'remove_dialog': state.say = ''; break;
        case 'move_x': state.x = cleanNumber(state.x + n(0)); break;
        case 'move_y': state.y = cleanNumber(state.y + n(0)); break;
        case 'locate_x': state.x = n(0); break;
        case 'locate_y': state.y = n(0); break;
        case 'locate_xy': state.x = n(0); state.y = n(1); break;
        case 'move_direction': { const distance = n(0), radians = (state.rotation + state.direction - 90) * Math.PI / 180; state.x = cleanNumber(state.x + Math.cos(radians) * distance); state.y = cleanNumber(state.y - Math.sin(radians) * distance); break; }
        case 'direction_absolute': state.direction = normalizeAngle(n(0)); break;
        case 'direction_relative': state.direction = normalizeAngle(state.direction + n(0)); break;
        case 'rotate_absolute': case 'rotate_relative': {
          const rotation = n(0) + (block.type === 'rotate_relative' ? state.rotation : 0);
          state.rotation = (object.object.rotateMethod ?? 'free') === 'free' ? normalizeAngle(rotation) : 0;
          break;
        }
        case 'show': state.visible = true; break;
        case 'hide': state.visible = false; break;
        case 'set_scale_size': state.size = Math.max(0, n(0)); break;
        case 'change_scale_size': state.size = Math.max(0, cleanNumber(state.size + n(0))); break;
        case 'message_cast': requireThat(pending.length < LIMITS.threads, 'Pending event limit exceeded'); pending.push({ tick: tick + 1, type: 'message', id: p[0] }); break;
        default: fail('Unsupported statement');
      }
    }
  }
  const start = (kind, value) => {
    for (const [objectId, object] of objects) for (const script of object.scripts) {
      const hat = script[0];
      if ((kind === 'start' && hat.type === 'when_run_button_click') ||
        (kind === 'keyDown' && hat.type === 'when_some_key_pressed' && Number(hat.params[1]) === value) ||
        (kind === 'click' && hat.type === 'when_object_click' && objectId === value) ||
        (kind === 'message' && hat.type === 'when_message_cast' && hat.params[1] === value)) {
        requireThat(threads.filter(thread => thread.alive).length < LIMITS.threads, 'Concurrent script limit exceeded');
        const thread = { objectId, alive: true, wake: tick };
        thread.iterator = runSequence(script.slice(1), thread); threads.push(thread);
      }
    }
  };
  start('start');
  let eventIndex = 0;
  for (tick = 0; tick < scenario.ticks; ++tick) {
    for (const variable of variables.values()) if (variable.type === 'timer') variable.value = tick / 60;
    while (eventIndex < scenario.events.length && scenario.events[eventIndex].tick === tick) {
      const event = scenario.events[eventIndex++];
      if (event.type === 'keyDown') { if (!keys.has(event.key)) start('keyDown', event.key); keys.add(event.key); }
      else if (event.type === 'keyUp') keys.delete(event.key);
      else start('click', event.objectId);
    }
    const due = pending; pending = [];
    for (const event of due) { if (event.tick <= tick) start(event.type, event.id); else pending.push(event); }
    for (const thread of [...threads]) if (thread.alive && thread.wake <= tick) {
      try { const result = thread.iterator.next(); thread.alive = !result.done; thread.wake = tick + (result.value ?? 1); }
      catch (error) { if (error === STOP) thread.alive = false; else if (error === BREAK || error === CONTINUE) fail('Loop control outside a loop'); else throw error; }
    }
    for (let index = threads.length - 1; index >= 0; --index) if (!threads[index].alive) threads.splice(index, 1);
    if (plan.mode === 'stdio' && threads.length === 0 && pending.length === 0) break;
  }
  if (plan.mode === 'stdio') requireThat(threads.length === 0 && pending.length === 0, 'Logical tick limit exceeded');
  const state = { version: 1, objects: dict(), variables: dict(), lists: dict() };
  for (const [id, object] of objects) state.objects[id] = { ...object.state };
  for (const [id, variable] of variables) state.variables[id] = variable.value;
  for (const [id, list] of lists) state.lists[id] = [...list.values];
  requireThat(bytes(JSON.stringify(state)) <= LIMITS.outputBytes, 'State output limit exceeded');
  return { stdout, state, steps, ticks: Math.min(tick + 1, scenario.ticks) };
}
