// Copyright (c) 2026 G74v3Dw31137
// SPDX-License-Identifier: MIT
// Adapted from Potter v0.2.0; see THIRD_PARTY.md.

// Synthetic interface fixture, NOT Google BotGuard and NOT a valid proof minter.
// Original BgUtils consumes this API so tests can inspect lifecycle/caching offline.
var fixtureMintCount=0;
var testGuard={a:function(program,setup){
 setup(function(callback,args){
  if(program==='snapshot-error'){callback('E:fixture');return}
  args[2][0]=async function(integrity){
   if(Array.from(integrity,function(b){return String.fromCharCode(b)}).join('')!=='TESTIT')throw Error('fixture invalid integrity');
   return async function(binding){
    var text=Array.from(binding,function(b){return String.fromCharCode(b)}).join('');
    if(text==='hang'){if(typeof fixtureMintStarted==='function')fixtureMintStarted();while(true){}}
    if(text==='reject')throw Error('PRIVATE fixture secret');
    return new TextEncoder().encode('fixture:'+(++fixtureMintCount)+':'+text);
   }
  };
  callback('fixture-snapshot:'+program);
 },function(){},function(){},function(){});
 return [function(){}];
}};
