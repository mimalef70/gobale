#!/usr/bin/env python3
"""Generate the checked-in OpenAPI route inventory; no runtime dependency."""
import argparse,copy,hashlib,json,re,sys,subprocess
import yaml
from pathlib import Path
root=Path(__file__).resolve().parents[1]
source=(root/'src/ui/rest/server.go').read_text()
app_version=re.search(r'const AppVersion = "([^"]+)"',(root/'src/config/settings.go').read_text())[1]
catalog=json.loads((root/'src/domains/operation_catalog.json').read_text())
by_operation={entry['operation']:entry for entry in catalog}
for entry in catalog:
 if entry['operation']=='account.name':
  entry['request']['properties']['push_name']=entry['request']['properties'].pop('name')
  entry['request']['required']=['push_name']
 if entry['operation']=='story.add':entry['request']['oneOf']=[{'required':['text']},{'required':['media_id']}]
 if entry['operation']=='send.template':entry['request']['oneOf']=[{'required':[key]} for key in ['inline_keyboard','reply_keyboard','remove_keyboard']]

# Export actual adapter contracts rather than maintaining a second capability list.
provider_catalogs=json.loads(subprocess.check_output(['go','run','./internal/providercatalog'],cwd=root/'src',text=True))
for entries in provider_catalogs.values():
 for entry in entries:
  if entry['operation']=='account.name' and 'name' in entry['request'].get('properties',{}):
   entry['request']['properties']['push_name']=entry['request']['properties'].pop('name')
   entry['request']['required']=['push_name']
all_provider_operations=[entry for entries in provider_catalogs.values() for entry in entries]
all_operation_names=sorted({entry['operation'] for entry in all_provider_operations})
extension_routes={(entry['method'],entry['path']):entry for entry in catalog}
for method,path,operation in re.findall(r'\{"(GET|POST|PUT|PATCH|DELETE)",\s*"([^"]+)",\s*"([^"]+)"\}',source):
 if operation in by_operation:extension_routes[(method,path)]=by_operation[operation]
routes=set((m.upper(),p) for m,p in re.findall(r'r\.(Get|Post|Put|Patch|Delete)\("([^"\n]+)"',source) if not p.endswith('/'))
# Loop-registered routes are explicit here and verified against app.GetRoutes in tests.
for kind in ['message','image','file','audio','video','voice']:routes.add(('POST','/send/'+kind))
for action in ['pause','resume','cancel']:routes.add(('POST','/send/schedules/:schedule_id/'+action))
for m,p,_ in re.findall(r'\{"(GET|POST|PUT|PATCH|DELETE)",\s*"([^"]+)",\s*"([^"]+)"\}',source):routes.add((m,p))
routes.update(extension_routes)
routes.update((method,'/ui/auth/session') for method in ['GET','POST','DELETE'])
peer={'type':'object','additionalProperties':False,'required':['type','id'],'properties':{'type':{'type':'string','enum':['user','group','channel','bot','service']},'id':{'type':'string','minLength':1,'maxLength':256,'description':'Opaque provider ID. The selected adapter validates its own format. Rubika supports user u0, group g0, channel c0, bot b0 and service s0 peers; Bale/Eitaa accept user/group/channel only. Eitaa group IDs distinguish classic groups (91) from channel-backed supergroups (channel_91); preserve the prefix. Eitaa user and broadcast-channel IDs remain decimal strings.'}}}
schedule_properties={
 'scheduled_at':{'type':'string','format':'date-time'},
 'timezone':{'type':'string','minLength':1,'description':'IANA timezone; required when scheduled_at is present.'},
 'recurrence':{'type':'string','enum':['none','','daily','weekly','monthly'],'description':'Omitted, empty or none means one occurrence. Use none for a one-time schedule.'},
 'weekdays':{'type':'array','items':{'type':'integer','minimum':0,'maximum':6}},
 'day_of_month':{'type':'integer','minimum':1,'maximum':31},
 'end_at':{'type':'string','format':'date-time'},
 'occurrence_limit':{'type':'integer','minimum':1},
}
send={'type':'object','additionalProperties':False,
 'oneOf':[{'required':['peer']},{'required':['phone']}],
 'properties':{
  'peer':{'$ref':'#/components/schemas/Peer'},
  'phone':{'type':'string','minLength':1,'description':'Bale-only exact phone lookup against provider contact information. Eitaa and Rubika require peer. Unknown or ambiguous matches fail; contacts are never imported implicitly.'},
  'kind':{'type':'string','enum':['text','image','file','audio','video','voice'],'description':'POST /send/schedules uses this kind (default text). Immediate /send/{kind} paths determine the kind themselves.'},
  'message':{'type':'string','maxLength':65536,'description':'Text body, required for text sends. Selected-provider limit: Bale 65536 UTF-8 bytes; Eitaa 4096 Unicode characters; Rubika 4200 Unicode characters. /app/providers exposes these limits with explicit units. Media routes use caption instead.'},
  'caption':{'type':'string','maxLength':65536,'description':'Media caption; same selected-provider limit as text. Bale allows 65536 UTF-8 bytes, Eitaa 4096 Unicode characters and Rubika 4200 Unicode characters. Not accepted on the text route.'},
  'ptt':{'type':'boolean','description':'Audio only: true selects verified Ogg Opus voice without conversion.'},
  'mentions':{'type':'array','maxItems':100,'uniqueItems':True,'items':{'type':'string','minLength':1,'maxLength':256},'description':'Currently supported by Bale only: canonical positive uint32 user IDs (maximum 4294967295), up to 100 distinct entries. Eitaa and Rubika reject nonempty mentions. Requires nonempty message for text or caption for media; part of the idempotency payload.'},
  'reply_message_id':{'type':'string','minLength':1,'maxLength':256,'description':'Original provider message ID. Bale uses a nonzero signed int64 string; preserve its sign.'},
  'media_id':{'type':'string','minLength':1},
  **schedule_properties,
 },
 'description':'Ordinary text or media send. Select exactly one destination. For rich messages use their typed operation endpoint, or ScheduleRequest for a delayed rich send. operation, payload and gateway request_id are not caller inputs here.',
}
ordinary_schedule=json.loads(json.dumps(send))
ordinary_schedule['required']=['scheduled_at','timezone']
ordinary_schedule['allOf']=[{'oneOf':[
 {'required':['message'],'properties':{'kind':{'enum':['text']},'message':{'minLength':1}},'not':{'anyOf':[{'required':['caption']},{'required':['ptt']},{'required':['media_id']}]}},
 {'required':['kind','media_id'],'properties':{'kind':{'enum':['image','file','audio','video','voice']}},'not':{'required':['message']},'anyOf':[{'properties':{'kind':{'enum':['audio']}}},{'not':{'required':['ptt']}}]},
]}]
schedulable_operations=[entry for entry in all_provider_operations if entry.get('schedulable')]
rich_send_names=sorted({entry['operation'] for entry in schedulable_operations})
rich_schedule={'type':'object','additionalProperties':False,
 'required':['scheduled_at','timezone','operation','payload'],
 'properties':{
  **schedule_properties,
  'peer':{'$ref':'#/components/schemas/Peer','description':'Optional. When supplied it must exactly equal payload.peer.'},
  'kind':{'type':'string','enum':['operation']},
  'operation':{'type':'string','enum':rich_send_names},
  'payload':{'type':'object'},
 },
 'anyOf':[{'properties':{'operation':{'enum':[entry['operation']]},'payload':entry['request']}} for entry in schedulable_operations],
 'description':'Schedules one reviewed rich message producer or message.forward. Forward stores the original source reference, not a content snapshot; availability is checked again at execution. Message fields belong inside payload; its peer determines the destination. Administrative operations and stories cannot be scheduled.',
}
schemas={'Peer':peer,'SendRequest':send,'ScheduledMessageRequest':ordinary_schedule,'ScheduledRichSendRequest':rich_schedule,'ScheduleRequest':{'oneOf':[{'$ref':'#/components/schemas/ScheduledMessageRequest'},{'$ref':'#/components/schemas/ScheduledRichSendRequest'}]},'Envelope':{'type':'object','required':['code','message'],'properties':{'code':{'type':'string'},'message':{'type':'string'},'results':{'description':'Endpoint result. IDs are strings; encrypted session material is never returned.'}}},'WebhookPatch':{'type':'object','properties':{'webhook_url':{'type':'string','description':'Empty string removes the device override.'},'webhook_secret':{'type':'string','writeOnly':True,'description':'An effective nonblank secret is required while webhook_url is nonempty; GET never returns it.'},'webhook_events':{'type':'array','items':{'type':'string'}}}},'Login':{'type':'object','required':['phone'],'properties':{'phone':{'type':'string'}}},'Code':{'type':'object','required':['challenge_id','code'],'properties':{'challenge_id':{'type':'string'},'code':{'type':'string','writeOnly':True}}},'Password':{'type':'object','required':['challenge_id','password'],'properties':{'challenge_id':{'type':'string'},'password':{'type':'string','writeOnly':True}}}}
decimal_id={'type':'string','pattern':'^[0-9]+$','description':'Positive decimal int64 encoded as a string.'}
timestamp={**decimal_id,'description':'Original provider timestamp in milliseconds, encoded as a decimal string.'}
peer_ref={'$ref':'#/components/schemas/Peer'}
users={'type':'array','items':peer_ref,'maxItems':100,'description':'Unique user peers known to this account; provider references are resolved internally.'}
title={'type':'string','minLength':1,'maxLength':255}
mutation_bodies={
 '/group':(['title'],{'title':title,'users':users,'kind':{'type':'string','enum':['group','channel','supergroup']},'username':{'type':'string','maxLength':64}}),
 '/group/participants':(['peer','users'],{'peer':peer_ref,'users':{**users,'minItems':1}}),
 '/group/name':(['peer','title'],{'peer':peer_ref,'title':title}),
 '/group/topic':(['peer','description'],{'peer':peer_ref,'description':{'type':'string','maxLength':4096,'description':'Required; an empty string clears the description.'}}),
 '/group/participants/remove':(['peer','user'],{'peer':peer_ref,'user':peer_ref}),
 '/message/{message_id}/update':(['peer','message'],{'peer':peer_ref,'message':{'type':'string','minLength':1,'maxLength':65536,'description':'UTF-8 text, at most 65536 bytes.'}}),
 '/message/{message_id}/read':(['peer','date'],{'peer':peer_ref,'date':{**timestamp,'description':'Marks messages read up to this provider timestamp. The path message ID is contextual; date is required.'}}),
 '/message/{message_id}/forward':(['peer','source_peer','source_date'],{'peer':peer_ref,'source_peer':peer_ref,'source_date':timestamp,'hide_sender':{'type':'boolean','default':False}}),
 '/message/{message_id}/delete':(['peer','just_mine'],{'peer':peer_ref,'just_mine':{'type':'boolean','description':'Required explicit scope: true removes from this account view; false requests everyone deletion, subject to provider permissions.'},'date':timestamp}),
}

# Response models follow the JSON tags and concrete REST return values. List
# results are arrays where the server returns arrays; do not invent pagination
# objects. Provider access hashes, session fields and webhook secrets stay private.
def ref(name):
 return {'$ref':'#/components/schemas/'+name}
def obj(properties, required=None, description=None):
 result={'type':'object','properties':properties}
 if required is not None:result['required']=required
 if description:result['description']=description
 return result
def array(item):
 return {'type':'array','items':ref(item) if isinstance(item,str) else item}
provider_id={'type':'string','enum':['bale','eitaa','rubika'],'description':'Immutable messenger for this connection; availability is reported by /app/providers.'}
opaque_id={'type':'string','minLength':1,'maxLength':256,'description':'Opaque ID validated by the selected provider. Preserve its string form.'}
text={'type':'string'}
date_time={'type':'string','format':'date-time'}
signed_id={'type':'string','pattern':'^-?[0-9]+$','description':'Nonzero signed int64 encoded as a decimal string. Preserve the sign.'}
alias={'type':'string','description':'Local device alias, not the provider account ID.'}
count={'type':'integer','format':'int64','minimum':0}
json_object={'type':'object','additionalProperties':True}
schemas['Envelope']=obj({'code':text,'message':text},['code','message'],'Common response fields. Results are omitted for empty responses and ordinary errors.')
schemas['EventPeer']=obj({'type':{'type':'string','enum':['user','group','channel','bot','service','']},'id':{'type':'string','maxLength':256}},['type','id'],'Connection-level events and group-creation requests can have empty type/id; message peers are populated.')
schemas['WebhookFilter']=obj({'peers':{'type':'array','maxItems':100,'uniqueItems':True,'items':ref('Peer')},'exclude_peers':{'type':'array','maxItems':100,'uniqueItems':True,'items':ref('Peer')},'peer_types':{'type':'array','uniqueItems':True,'items':{'type':'string','enum':['user','group','channel','bot','service']}},'sender_ids':{'type':'array','maxItems':100,'uniqueItems':True,'items':opaque_id},'exclude_sender_ids':{'type':'array','maxItems':100,'uniqueItems':True,'items':opaque_id},'directions':{'type':'array','uniqueItems':True,'items':{'type':'string','enum':['incoming','outgoing','unknown']}}},[], 'OR within each list, AND across fields; exclusions win. Missing peer/sender metadata cannot satisfy corresponding include lists. Missing or invalid direction provenance is unknown and matches only an explicit unknown direction include. IDs are validated by the selected provider; Bale requires canonical positive uint32 strings. Eitaa supergroup peers use type group with id channel_91, while classic groups use id 91; Eitaa sender IDs remain numeric strings. Empty lists impose no constraint. Device destination only; existing queued deliveries are unchanged.')
schemas['WebhookFilter']['additionalProperties']=False
filter_user_id={**opaque_id,'description':'Provider sender actor ID. Bale requires canonical positive uint32 decimal strings; Rubika accepts u0 user, b0 bot and s0 service GUIDs. Eitaa sender IDs are decimal strings.'}
filter_peer=copy.deepcopy(peer)
filter_peer['properties']['id']=copy.deepcopy(peer['properties']['id'])
for key in ['sender_ids','exclude_sender_ids']:schemas['WebhookFilter']['properties'][key]['items']=filter_user_id
for key in ['peers','exclude_peers']:schemas['WebhookFilter']['properties'][key]['items']=filter_peer
schemas['WebhookPatch']['properties']['webhook_filter']={**ref('WebhookFilter'),'description':'Omit to preserve, {} to clear; null is rejected. A supplied object replaces the entire filter. Changes affect new events and explicit replay only.'}
schemas['WebhookConfig']=obj({'webhook_url':text,'webhook_events':array(text),'webhook_filter':ref('WebhookFilter'),'revision':{'type':'integer','format':'int64','minimum':1}},['webhook_url','webhook_events','webhook_filter','revision'],'The secret is never returned. An empty URL selects global fallback routing.')
schemas['Device']=obj({'id':alias,'provider':provider_id,'instance_id':{'type':'string','description':'Opaque immutable connection instance. Send X-Device-Instance to reject a deleted/recreated alias.'},'account_id':opaque_id,'created_at':date_time,'webhook':ref('WebhookConfig')},['id','provider','instance_id','created_at','webhook'])
schemas['ConnectionStatus']=obj({
 'auth':{'type':'string','enum':['unauthenticated','awaiting_code','awaiting_password','authenticated','auth_required']},
 'transport':{'type':'string','enum':['disconnected','connecting','handshaking','connected']},
 'recovery':{'type':'string','enum':['not_started','degraded','recovering','current','gap_detected']},
 'recovery_issue':{'type':'string','enum':['legacy_unclassified','chat_state_expired','message_state_expired','difference_too_long','channel_difference_too_long','unresolved_message_peer'],'description':'Provider-scoped durable recovery provenance. Legacy mixed warnings remain unclassified; channel invalidations and unsupported update coverage alone do not prove expired recovery state.'},
 'unsupported_updates_observed':{'type':'boolean','description':'Unsupported namespace/content accepted during this client lifetime. Reset on client recreation; durable events provide the audit history. Independent of recovery gaps.'},
 'last_error':text},['auth','transport','recovery'],'Transport connected does not imply recovery is current.')
schemas['DeliveryCounts']=obj({'pending':count,'failed':count,'paused':count},['pending','failed','paused'])
schemas['DeviceOverview']={'allOf':[ref('Device'),obj({'status':ref('ConnectionStatus'),'deliveries':ref('DeliveryCounts')},['status','deliveries'])]}
schemas['DevicesOverview']=obj({'server_time':date_time,'devices':array('DeviceOverview')},['server_time','devices'],'Local database/client snapshots only; no provider RPC or implicit reconnect.')
schemas['PublicChallenge']=obj({'challenge_id':text,'expires_at':date_time,'resend_available_at':date_time,'delivery':text,'next_delivery':text,'available_deliveries':array(text),'masked_phone':text},['challenge_id','expires_at','resend_available_at','masked_phone'])
schemas['DeviceStatus']={'allOf':[ref('ConnectionStatus'),obj({'device_id':alias,'provider':provider_id,'instance_id':{'type':'string','pattern':'^[0-9a-f]{64}$'},'account_id':{'type':'string','description':'Bound provider account ID, or empty before first authentication. Logout retains the binding.'},'challenge':{'allOf':[ref('PublicChallenge')],'nullable':True},'server_time':date_time},['device_id','provider','instance_id','account_id','challenge','server_time'])],'description':'Local snapshot with connection, authentication, recovery and safe challenge metadata. No provider RPC or implicit reconnect. Expired challenges are null and cannot advertise an awaiting auth state.'}
schemas['LoginState']=obj({'state':text,'challenge':{'allOf':[ref('PublicChallenge')],'nullable':True},'server_time':date_time},['state','challenge','server_time'],'Pending challenge metadata only; cleared on expiry, authentication, logout or server restart. No OTP/password/provider transaction hash is returned.')
schemas['WebhookRoutingRule']=obj({'source':{'type':'string','enum':['device','global']},'url':text,'events':array(text),'filter':ref('WebhookFilter'),'secret_configured':{'type':'boolean'}},['source','url','events','filter','secret_configured'])
schemas['WebhookDetails']={'allOf':[ref('WebhookConfig'),obj({'secret_configured':{'type':'boolean'},'routing_mode':{'type':'string','enum':['none','device','global','merged']},'routing_rules':array('WebhookRoutingRule')},['secret_configured','routing_mode','routing_rules'])]}
schemas['BrowserSession']=obj({'csrf_token':text,'expires_at':date_time,'absolute_expires_at':date_time},['csrf_token','expires_at','absolute_expires_at'],'In-memory administrative browser session; restart invalidates it without logging out messenger accounts. Send the CSRF token on browser mutations.')
schemas['LoginChallenge']=obj({'challenge_id':text,'state':{'type':'string','enum':['awaiting_code','awaiting_password']},'expires_at':date_time,'delivery':text,'next_delivery':text,'resend_after_seconds':{'type':'integer','minimum':0},'available_deliveries':array(text)},['challenge_id','state'])
stored_properties={**send['properties'],'peer':ref('EventPeer'),'kind':{'type':'string','enum':['text','image','file','audio','video','voice','operation']},'request_id':{**decimal_id,'readOnly':True,'description':'Gateway-assigned persistent wire request ID.'},'operation':text,'payload':{**json_object,'description':'Normalized mutation arguments. Present only for kind=operation.'}}
for public_only in ['caption','ptt']:stored_properties.pop(public_only,None)
stored_properties['message']={**stored_properties['message'],'description':'Canonical persisted text or media caption. HTTP media input uses caption; accepted storage hashes retain message.'}
schemas['StoredRequest']=obj(stored_properties,['peer'],'Persisted request snapshot, including the gateway-assigned request_id. Mutation arguments are in payload.')
schemas['MutationResult']=obj({'acknowledged':{'type':'boolean'},'peer':ref('Peer'),'title':text,'invite_link':text,'not_added_user_ids':array(opaque_id),'message_id':opaque_id,'date':date_time,'just_mine':{'type':'boolean'}},description='Provider-specific mutation result. The named fields below describe Bale; other providers return their reviewed operation projection and may use different fields. Group creation returns peer/title/invite_link/not_added_user_ids; invites return acknowledged/not_added_user_ids; forward returns acknowledged/message_id/date; delete returns acknowledged/message_id/just_mine; other implemented mutations return acknowledged.')
schemas['SendResult']=obj({'message_id':opaque_id,'date':date_time,'data':ref('MutationResult')},description='Text/media acknowledgements have message_id and date. Provider mutations put their result in data and omit zero message_id/date. Acceptance is not recipient delivery or read confirmation.')
schemas['OperationStage']=obj({'number':{'type':'integer','minimum':1,'maximum':32},'name':{'type':'string','enum':['upload','send','forward','poll.create','poll.send','profile.photo','album.send']},'state':{'type':'string','enum':['started','succeeded','failed','unknown']},'started_at':date_time,'updated_at':date_time},['number','name','state','started_at','updated_at'],'Durable compound progress. Upload capabilities, request nonces and private provider results are never exposed.')
schemas['Operation']=obj({'send_id':{'type':'string','format':'uuid'},'device_id':alias,'provider':provider_id,'request':ref('StoredRequest'),'stages':{'type':'array','items':ref('OperationStage')},'state':{'type':'string','enum':['queued','sending','succeeded','failed','unknown','cancelled']},'message_id':opaque_id,'date':date_time,'data':ref('MutationResult'),'status':text,'error_code':text,'error_message':text,'schedule_id':{'type':'string','format':'uuid'},'scheduled_for':date_time,'created_at':date_time,'updated_at':date_time},['send_id','device_id','provider','request','state','status','created_at','updated_at'],'Only succeeded operations expose message_id/date/data, directly beside send_id and state. Unknown means the provider outcome is unresolved; do not blindly resend. Only reviewed provider-specific evidence may reconcile it to succeeded; text matching or ordinary receipts are insufficient.')
schemas['Schedule']=obj({'id':{'type':'string','format':'uuid'},'device_id':alias,'provider':provider_id,'request':ref('StoredRequest'),'status':{'type':'string','enum':['active','paused','cancelled','completed','failed']},'next_run_at':date_time,'occurrence_count':count,'occurrence_history_complete':{'type':'boolean','description':'False if this schedule includes executions before authoritative occurrence tracking was introduced. Completed means no future occurrences, not successful provider delivery.'},'created_at':date_time},['id','device_id','provider','request','status','next_run_at','occurrence_count','occurrence_history_complete','created_at'])
schemas['ScheduleOccurrence']=obj({'schedule_id':{'type':'string','format':'uuid'},'occurrence_number':{'type':'integer','minimum':1},'scheduled_for':date_time,'operation':ref('Operation')},['schedule_id','occurrence_number','scheduled_for','operation'])
schemas['MessagePayload']=obj({'kind':{'type':'string','enum':['text','document','contact','location','template','template_response','service','sticker','gift','gold_gift','poll','empty','forward','unsupported']},'message':text,'file_id':opaque_id,'size':count,'name':text,'mime_type':text,'caption':text,'media_type':{'type':'string','enum':['image','video','voice','audio','animation']},'duration':{'type':'integer','minimum':0,'description':'Provider media duration when available. Native voice duration is in milliseconds; do not assume all media variants use the same unit.'},'download_supported':{'type':'boolean'}},['kind'],'Normalized bounded content. Optional quote, content, keyboard, service actions and poll results are described in the [webhook payload guide](https://github.com/mimalef70/goomni/blob/main/docs/webhook-payload.md). Access hashes and raw opaque protocol data are never exposed.')
schemas['MessageMedia']=obj({'type':{'type':'string','enum':['file','image','video','audio','voice','animation','sticker']},'file_id':text,'name':text,'mime_type':text,'size':count,'download_supported':{'type':'boolean'}},['type','file_id','name','mime_type','size','download_supported'],'Attachment metadata remains present even when no private download reference exists or a later download fails. Empty strings mean metadata is unavailable. Download support is not a guarantee of future provider availability.')
schemas['MessageOrigin']=obj({'message_id':opaque_id,'sender_id':opaque_id,'peer':ref('EventPeer'),'date':text,'author_sign':text},description='Reviewed original-message provenance. This does not change the forwarding sender or grant attachment access.')
schemas['Message']=obj({'partial':{'type':'boolean','enum':[False]},'id':opaque_id,'chat_id':text,'from':text,'sender_display_name':text,'sender_name_status':{'type':'string','enum':['available','unavailable']},'is_from_me':{'type':'boolean','nullable':True},'timestamp':date_time,'body':text,'kind':text,'supported':{'type':'boolean'},'replied_to_id':opaque_id,'quoted_body':text,'original_message_id':opaque_id,'editor_id':opaque_id,'forwarded_from':ref('MessageOrigin'),'media':ref('MessageMedia')},['partial','id','chat_id','from','sender_display_name','sender_name_status','is_from_me','timestamp','body','kind','supported'],'Consumer-ready projection for message/edit events. Message and sender IDs retain their provider string values. chat_id equals envelope peer.id for newly projected events and history in all providers. Route by provider, immutable connection and the full peer type/id pair; IDs are opaque strings. Full edits carry original_message_id equal to envelope message_id. partial=false identifies a full projection; partial edits use MessagePatch. Do not coerce IDs to numbers. For Bale, incoming event names are enriched before durable acceptance using account-scoped cached names, contacts, a bounded recent-dialog scan and verified profile reads. Cold lookups wait for the next per-connection one-second slot and share a 500ms provider-read deadline; queueing can add delay. Missing, inaccessible, unsafe or timed-out names are empty/unavailable. History uses available cached names. Later cache fills do not rewrite persisted bodies or emit name-completion events. Unknown direction has null is_from_me. Bale edits keep from/name/direction unknown and expose the verified updater separately as editor_id; other adapters preserve original-author fields only when established by the reviewed message object. Body is plain Unicode text or a display fallback, never HTML. Older persisted events retain their exact bytes and may omit this projection.')
schemas['MessagePatch']=obj({'id':opaque_id,'chat_id':opaque_id,'original_message_id':opaque_id,'partial':{'type':'boolean','enum':[True]},'body':text,'supported':{'type':'boolean'}},['id','chat_id','original_message_id','partial','supported'],'Reviewed partial edit. Identity matches envelope message_id and peer.id. An absent body leaves text unchanged; an explicitly empty body clears text. Never replace an entire message, sender, direction, timestamp, media or reply from this patch. supported means a reviewed body change is available; false is a diagnostic-only patch. Native metadata stays in content. Refresh current message/media as needed; no original author/time or attachment availability is inferred.')
receipt_date={**text,'pattern':'^[0-9]+$','description':'Nonnegative provider milliseconds as a decimal string. Zero is preserved, never replaced with the envelope time.'}
schemas['ReceiptPayload']=obj({'start_date':{**receipt_date,'description':'Provider startDate; the reviewed web client uses this as a cumulative peer watermark for read/received events, not the lower endpoint of a closed range. Exact message coverage and boundary inclusion are not live-verified.'},'read_date':{**receipt_date,'description':'Field 3 of message.read: provider readDate, not the end of a message range.'},'received_date':{**receipt_date,'description':'Field 3 of message.received: provider receivedDate, not the end of a message range.'},'end_date':{**receipt_date,'description':'Optional wrapped endDate of message.read_by_me only; omission and explicit zero remain distinct.'},'unread_count':count,'message_ids_supported':{'type':'boolean','enum':[False]}},['start_date','message_ids_supported'],'Bale receipt timestamps, not a shared cross-provider receipt shape or validated closed ranges or exact message-ID acknowledgments. message.read includes read_date; message.received includes received_date; message.read_by_me may include end_date/unread_count. No inferred receipt IDs or unknown-send reconciliation. Already persisted historical bodies remain unchanged during retry/replay.')
schemas['Event']=obj({'event_id':text,'provider':provider_id,'event':{'type':'string','description':'Examples: message, message.edited, message.deleted, message.accepted, connection.recovery, protocol.unsupported_update.'},'device_id':{'type':'string','description':'Provider account ID; unlike Operation.device_id, this is not the local alias.'},'session_id':alias,'instance_id':{'type':'string','pattern':'^[0-9a-f]{64}$','description':'Immutable connection identity on newly accepted events. Bodies already persisted before this field was introduced are preserved on retry/replay and may omit it.'},'peer':ref('EventPeer'),'message_id':opaque_id,'sender_id':opaque_id,'direction':{'type':'string','enum':['incoming','outgoing','unknown']},'timestamp':date_time,'content':{**json_object,'description':'Reviewed provider-specific content on projected message/edit events. MessagePayload describes Bale content; other adapters retain their reviewed native metadata here. No provider credentials or opaque raw bytes.'},'payload':{**json_object,'description':'Consumer-ready Message on complete message/edit events or MessagePatch on partial edits; provider-specific receipts (ReceiptPayload for Bale) on receipt events; reviewed event-specific metadata on other families. Historical signed bodies retain their original layout during retry/replay.'}},['event_id','event','device_id','session_id','peer','timestamp','payload'])
schemas['Delivery']=obj({'delivery_id':{'type':'string','format':'uuid'},'event_id':text,'device_id':alias,'url':{'type':'string','format':'uri'},'revision':{'type':'integer','format':'int64'},'payload':{'type':'object','nullable':True,'allOf':[ref('Event')],'description':'Null only when list include_payload=false; detail includes the full event.'},'state':{'type':'string','enum':['queued','delivering','retry','delivered','failed','paused','cancelled']},'attempts':count,'next_attempt_at':date_time,'last_error':text,'created_at':date_time},['delivery_id','event_id','device_id','url','revision','payload','state','attempts','next_attempt_at','created_at'],'The signing secret is never returned. HTTP 4xx except 408/425/429 is permanent; other unsuccessful responses and network errors retry up to eight normally completed attempts. Failed deliveries release later queued events and retain replayable history. Retry creates a new delivery ledger at the original queue position; replay appends new deliveries using current routing. Event identity remains stable.')
schemas['LocalChat']=obj({'peer':ref('Peer'),'last_event':ref('Event'),'count':count},['peer','last_event','count'],'Local persisted event aggregate; count is the number of stored events for the peer.')
schemas['RemoteChat']=obj({'peer':ref('Peer'),'unread_count':count,'date':date_time,'message_id':opaque_id,'payload':ref('MessagePayload')},['peer','unread_count','date','payload'])
schemas['RemoteChats']=obj({'chats':array('RemoteChat'),'next_date':timestamp},['chats'],'Use next_date as the next remote date cursor; absent when the returned page is shorter than limit.')
schemas['HistoryMessage']=obj({'peer':ref('Peer'),'message_id':opaque_id,'sender_id':opaque_id,'date':date_time,'direction':{'type':'string','enum':['incoming','outgoing']},'provider_state':{'type':'integer','format':'int32'},'payload':ref('MessagePayload'),'message':ref('Message')},['peer','message_id','sender_id','date','direction','provider_state','payload','message'])
schemas['HistoryPage']=obj({'messages':array('HistoryMessage'),'next_date':{**timestamp,'description':'Directional cursor for a full advancing forward/backward page; absent for around mode, a short page, or a stopped incomplete page.'},'before_date':{**timestamp,'description':'Oldest timestamp returned in around mode; not a promise that another page exists.'},'after_date':{**timestamp,'description':'Newest timestamp returned in around mode; not a promise that another page exists.'},'incomplete':{'type':'boolean','description':'True when a full directional page cannot advance the date cursor. Retain and deduplicate returned messages; completeness is not established.'},'pagination_stop_reason':{'type':'string','enum':['non_advancing_cursor'],'description':'Stop automatic pagination when present. next_date is omitted; changing the timestamp artificially could skip messages.'}},['messages'])
schemas['Media']=obj({'id':{'type':'string','format':'uuid'},'name':text,'content_type':text,'size':count,'created_at':date_time},['id','name','content_type','size','created_at'],'Local upload metadata. Filesystem paths are private.')
schemas['Contact']=obj({'peer':ref('Peer'),'name':text,'username':text,'is_bot':{'type':'boolean'},'is_deleted':{'type':'boolean'}},['peer'],'Reference-only entries can contain only peer.')
schemas['ContactGroup']=obj({'peer':ref('Peer'),'title':text},['peer','title'])
schemas['Contacts']=obj({'contacts':array('Contact'),'groups':array('ContactGroup')},['contacts','groups'])
schemas['GroupList']=obj({'groups':array('Peer')},['groups'])
schemas['GroupMember']=obj({'user_id':decimal_id,'inviter_id':decimal_id,'joined_at':date_time,'is_admin':{'type':'boolean'}},['user_id','inviter_id','joined_at','is_admin'])
schemas['GroupMembers']=obj({'members':array('GroupMember'),'next':text},['members','next'],'next is the opaque cursor for the following page; an empty string indicates no cursor.')
schemas['GroupInfo']=obj({'peer':ref('Peer'),'title':text,'description':text,'username':text,'owner_id':decimal_id,'created_at':date_time,'members_count':count,'is_member':{'type':'boolean'},'group_type':{'type':'integer','format':'int32'}},['peer','title','description','username','owner_id','created_at','members_count','is_member','group_type'])
schemas['InviteLink']=obj({'invite_link':text},['invite_link'])
schemas['AccountInfo']=obj({'account_id':decimal_id,'name':text,'username':text,'about':text,'phone':text,'is_bot':{'type':'boolean'},'is_deleted':{'type':'boolean'}},['account_id','name','username','about','phone','is_bot','is_deleted'])
schemas['ProviderSend']=obj({'kinds':{'type':'array','maxItems':6,'uniqueItems':True,'items':{'type':'string','enum':['text','file','image','audio','voice','video']}},'max_text_bytes':{'type':'integer','minimum':1,'description':'UTF-8 byte limit for message text and media captions. Present for Bale; not a character count.'},'max_text_characters':{'type':'integer','minimum':1,'description':'Unicode code-point limit for message text and media captions. Present for Eitaa and Rubika; not a UTF-8 byte count.'},'mentions_supported':{'type':'boolean'},'max_mentions':{'type':'integer','minimum':0,'maximum':100},'reply_supported':{'type':'boolean'},'media_format_notes':obj({kind:{'type':'string','maxLength':512} for kind in ['voice','audio','video']})},['kinds','mentions_supported','max_mentions','reply_supported','media_format_notes'],'Adapter admission for ordinary text/media sends, including scheduled sends. Empty kinds means no registered send implementation. Media notes describe validation rather than live provider acceptance; consult verification and enabled before use. Ordinary sends are separate from named extension operations.')
schemas['OperationPreset']=obj({'operation':text,'parameters':json_object},['operation','parameters'],'A reviewed public operation and fixed public parameters. Resolve its method/path/mode from the selected provider operation catalogue. Merge with the selected peer when required; obey instance guards and idempotency requirements. This is not an arbitrary RPC or a promise of provider acceptance.')
schemas['ProviderInteractions']=obj({'typing':ref('OperationPreset'),'typing_stop':ref('OperationPreset'),'online':ref('OperationPreset'),'offline':ref('OperationPreset'),'read_operation':text,'read_argument':{'type':'string','enum':['date','message_id']},'receipt_model':{'type':'string','enum':['none','timestamp_watermark','message_id_watermark','chat_state']},'read_events':{'type':'boolean'},'delivered_events':{'type':'boolean'},'sender_names':{'type':'string','enum':['enriched','unavailable']},'avatar_download':{'type':'boolean'},'partial_edits':{'type':'boolean'}},['receipt_model','read_events','delivered_events','sender_names','avatar_download','partial_edits'],'Adapter semantics, independent of live verification. Missing operation presets mean unsupported. Read marking is distinct from receiving read events. Timestamp/message-ID watermarks are cumulative provider observations, not exact message acknowledgements; boundary inclusion is not asserted. Rubika chat state is not a delivered/read receipt. enriched names may still be unavailable per message. Partial edits never imply a complete replacement.')
schemas['Provider']=obj({'id':provider_id,'name':text,'enabled':{'type':'boolean'},'verification':text,'delivery_methods':array(text),'send':ref('ProviderSend'),'interactions':ref('ProviderInteractions')},['id','name','enabled','verification','delivery_methods','send','interactions'],'Runtime availability, ordinary send admission and verification scope. Catalogue entries do not imply live compatibility.')
schemas['ProviderList']=array('Provider')
schemas['ProviderListResponse']=obj({'code':text,'message':text,'results':ref('ProviderList')},['code','message','results'])
schemas['AppInfo']=obj({'name':text,'version':text,'release_stage':text,'providers':array('Provider'),'capabilities':obj({'multi_device':{'type':'boolean'},'multi_provider':{'type':'boolean'},'per_device_webhook':{'type':'boolean'},'durable_outbox':{'type':'boolean'},'scheduled_sends':{'type':'boolean'}},['multi_device','multi_provider','per_device_webhook','durable_outbox','scheduled_sends']),'protocol_note':text},['name','version','release_stage','providers','capabilities','protocol_note'])
schemas['Health']=obj({'status':{'type':'string','enum':['ok']}},['status'])
schemas['Ready']=obj({'status':{'type':'string','enum':['ready']}},['status'])
for name,item in {'OperationList':'Operation','ScheduleOccurrenceList':'ScheduleOccurrence','DeviceList':'Device','ScheduleList':'Schedule','EventList':'Event','DeliveryList':'Delivery','LocalChatList':'LocalChat'}.items():
 schemas[name]=array(item)
for name in ['RemoteChat','RemoteChats','HistoryMessage','HistoryPage','Contacts','GroupList','GroupMember','GroupMembers','GroupInfo','AccountInfo']:
 if name in schemas:
  schemas[name]['x-goomni-provider']='bale'
  schemas[name]['description']='Bale result projection. Other providers use their documented native result envelope. '+schemas[name].get('description','')
for name in ['OperationList','ScheduleOccurrenceList','DevicesOverview','LoginState','WebhookDetails','BrowserSession','Device','DeviceList','DeviceStatus','ConnectionStatus','LoginChallenge','WebhookConfig','Operation','Schedule','ScheduleList','EventList','Delivery','DeliveryList','LocalChatList','RemoteChats','HistoryPage','Media','Contacts','GroupList','GroupMembers','GroupInfo','InviteLink','AccountInfo','AppInfo','Health','Ready']:
 schemas[name+'Response']=obj({'code':text,'message':text,'results':ref(name)},['code','message','results'])
schemas['EmptyResponse']={**obj({'code':text,'message':text},['code','message']),'additionalProperties':False}
schemas['ErrorResponse']={**obj({'code':text,'message':text},['code','message']),'additionalProperties':False}
schemas['ScheduledSend']=obj({'status':{'type':'string','enum':['Message scheduled']},'schedule_id':{'type':'string','format':'uuid'},'scheduled_at':date_time,'next_run_at':date_time},['status','schedule_id','scheduled_at','next_run_at'],'Acknowledges creation of a local schedule, not sending or recipient delivery. GET /send/schedules/{schedule_id} returns its current state.')
schemas['SendOrScheduleResponse']=obj({'code':text,'message':text,'results':{'oneOf':[ref('Operation'),ref('ScheduledSend')]}},['code','message','results'],'A scheduled_at request returns ScheduledSend immediately; an immediate send returns an Operation.')
schemas['ChatsResponse']=obj({'code':text,'message':text,'results':{'oneOf':[ref('LocalChatList'),ref('RemoteChats')]}},['code','message','results'],'source=local returns an array of LocalChat; source=remote returns {chats,next_date}.')
def response(name, description='Success'):
 return {'description':description,'content':{'application/json':{'schema':ref(name)}}}

success_models={
 ('GET','/send/operations'):'OperationListResponse',('GET','/send/schedules/{schedule_id}/occurrences'):'ScheduleOccurrenceListResponse',('GET','/events'):'EventListResponse',
 ('GET','/devices/overview'):'DevicesOverviewResponse',('GET','/devices/{device_id}/login'):'LoginStateResponse',
 ('GET','/ui/auth/session'):'BrowserSessionResponse',('POST','/ui/auth/session'):'BrowserSessionResponse',('DELETE','/ui/auth/session'):'EmptyResponse',
 ('GET','/app/providers'):'ProviderListResponse',
 ('GET','/health'):'HealthResponse',('GET','/ready'):'ReadyResponse',('GET','/app/info'):'AppInfoResponse',
 ('GET','/devices'):'DeviceListResponse',('GET','/app/devices'):'DeviceListResponse',('POST','/devices'):'DeviceResponse',('GET','/devices/{device_id}'):'DeviceResponse',('DELETE','/devices/{device_id}'):'EmptyResponse',
 ('GET','/devices/{device_id}/status'):'DeviceStatusResponse',('GET','/app/status'):'DeviceStatusResponse',('POST','/devices/{device_id}/login'):'LoginChallengeResponse',('POST','/devices/{device_id}/login/code'):'ConnectionStatusResponse',('POST','/devices/{device_id}/login/password'):'ConnectionStatusResponse',('POST','/devices/{device_id}/reconnect'):'EmptyResponse',('POST','/devices/{device_id}/logout'):'EmptyResponse',
 ('GET','/devices/{device_id}/webhook'):'WebhookDetailsResponse',('PATCH','/devices/{device_id}/webhook'):'WebhookConfigResponse',
 ('GET','/send/operations/{send_id}'):'OperationResponse',('GET','/send/schedules'):'ScheduleListResponse',('POST','/send/schedules'):'ScheduleResponse',('GET','/send/schedules/{schedule_id}'):'ScheduleResponse',
 ('GET','/deliveries'):'DeliveryListResponse',('GET','/deliveries/{delivery_id}'):'DeliveryResponse',('POST','/deliveries/{delivery_id}/retry'):'EmptyResponse',('POST','/deliveries/{delivery_id}/replay'):'DeliveryListResponse',
 ('GET','/chats'):'ChatsResponse',('GET','/chat/{chat_jid}/messages'):'EventListResponse',('GET','/chat/{chat_jid}/history'):'HistoryPageResponse',('POST','/media'):'MediaResponse',('POST','/media/fetch'):'MediaResponse',
 ('GET','/user/search'):'ContactsResponse',('GET','/user/my/contacts'):'ContactsResponse',('GET','/user/info'):'AccountInfoResponse',('GET','/user/my/groups'):'GroupListResponse',('GET','/group/participants'):'GroupMembersResponse',('GET','/group/info'):'GroupInfoResponse',('GET','/group/invite-link'):'InviteLinkResponse',
}
for kind in ['message','image','file','audio','video','voice']:success_models[('POST','/send/'+kind)]='SendOrScheduleResponse'
for action in ['pause','resume','cancel']:success_models[('POST','/send/schedules/{schedule_id}/'+action)]='EmptyResponse'
for path in mutation_bodies:success_models[('POST',path)]='OperationResponse'
unsupported_paths={'/user/check','/user/pushname','/message/{message_id}/revoke','/message/{message_id}/reaction','/group/participants/promote','/group/participants/demote','/group/join-with-link','/group/leave','/group/photo','/send/sticker','/send/poll','/send/contact','/send/location','/send/presence','/send/chat-presence'}
schemas['NativeResultResponse']=obj({'code':text,'message':text,'results':json_object},['code','message','results'],'Operation-specific normalized results; see the operation and tag descriptions. Provider access hashes and session credentials are omitted. Mini app URL/hash endpoints intentionally return short-lived launch credentials and must not be logged.')
schemas['Capability']=obj({'operation':text,'method':text,'path':text,'mode':{'type':'string','enum':['read','mutation','ephemeral']},'description':text,'verification':text,'schedulable':{'type':'boolean'},'request':json_object},['operation','method','path','mode','description','verification','schedulable','request'])
schemas['CapabilitiesResponse']=obj({'code':text,'message':text,'results':array('Capability')},['code','message','results'])
success_models[('GET','/app/capabilities')]='CapabilitiesResponse'
success_models[('GET','/devices/{device_id}/capabilities')]='CapabilitiesResponse'
schemas['NativeOperationResponse']={'anyOf':[ref('OperationResponse'),ref('NativeResultResponse')],'description':'Mutation mode returns the durable Operation envelope. Read and ephemeral modes return the native result envelope. The operation path selects the mode; these result shapes can overlap.'}
success_models[('POST','/operations/{operation}')]='NativeOperationResponse'
for (method,path),entry in extension_routes.items():
 path=re.sub(r':([a-z_]+)',r'{\1}',path)
 success_models[(method,path)]='OperationResponse' if entry['mode']=='mutation' else 'NativeResultResponse'
route_operations={(m,re.sub(r':([a-z_]+)',r'{\1}',p)):name for m,p,name in re.findall(r'\{"(GET|POST|PUT|PATCH|DELETE)",\s*"([^"]+)",\s*"([^"]+)"\}',source)}
route_operations.update({('GET','/chat/{chat_jid}/history'):'chat.history',('GET','/chats'):'chat.list'})
for (m,p),entry in extension_routes.items():route_operations.setdefault((m,re.sub(r':([a-z_]+)',r'{\1}',p)),entry['operation'])
paths={}
for method,p in sorted(routes):
 path=re.sub(r':([a-z_]+)',r'{\1}',p)
 extension=extension_routes.get((method,p))
 op={'operationId':method.lower()+'_'+re.sub(r'[^a-zA-Z0-9]+','_',p).strip('_'),'summary':method+' '+path,'security':[] if path=='/health' else [{'basicAuth':[]}],'responses':{'200':{'description':'Operation completed (send: provider acknowledgement only)','content':{'application/json':{'schema':{'$ref':'#/components/schemas/Envelope'}}}},'400':{'description':'Invalid request'},'401':{'description':'Administrative authentication required'},'404':{'description':'Device or scoped resource not found'},'409':{'description':'State or idempotency conflict'},'429':{'description':'CONNECTION_QUEUE_FULL, QUEUE_FULL or RESOURCE_BUSY: bounded connection, provider or global admission capacity exhausted; no new work was accepted.'},'501':{'description':'FEATURE_NOT_SUPPORTED: provider capability is unverified or unavailable'},'504':{'description':'REQUEST_TIMEOUT: synchronous JSON request deadline exceeded. Durable accepted sends continue independently.'}}}
 if (method=='GET' and (path.startswith('/media/') or path=='/user/avatar' or (path.startswith('/message/') and path.endswith('/download')))) or (method=='POST' and path=='/media'):op['responses']['504']={'description':'REQUEST_TIMEOUT: media admission queue exceeded its bounded wait. Once acquired, the stream is exempt from the synchronous JSON deadline.'}
 op['parameters']=[{'name':x,'in':'path','required':True,'schema':{'type':'string'}} for x in re.findall(r'{([^}]+)}',path)]
 for parameter in op['parameters']:
  if parameter['name']=='message_id':parameter['schema']=opaque_id
  if parameter['name']=='chat_jid':parameter['schema']={'type':'string','pattern':'^(user|group|channel|bot|service):[^\\s]+$','maxLength':264,'description':'Scoped type:id peer key. Provider access references are resolved inside the selected account.'}
 if not path.startswith(('/devices','/ui/')) and path not in ['/health','/ready','/app/info','/app/devices','/app/providers','/app/capabilities','/metrics']:
  op['parameters'] += [{'name':'X-Device-Id','in':'header','schema':{'type':'string'},'description':'Device alias. Supply this header or device_id query explicitly, even with one account. Conflicting path/header/query selectors return 400 DEVICE_SELECTOR_CONFLICT; invalid aliases never fall back. The request stays bound to the selected immutable connection; deletion never redirects it to a reused alias.'},{'name':'device_id','in':'query','schema':{'type':'string'}}]
 if ('{device_id}' in path or any(p['name']=='X-Device-Id' for p in op['parameters'])) and not path.startswith('/ui/'):
  op['parameters'].append({'name':'X-Device-Instance','in':'header','required':True,'schema':{'type':'string'},'description':'Required on all account-scoped machine and browser requests. Missing is 400 DEVICE_INSTANCE_REQUIRED. Must match the selected device instance_id; mismatch is 409 DEVICE_INSTANCE_CHANGED before any provider call. This precondition is not user authorization.'})
 if path=='/ui/auth/session':
  op['security']=[] if method=='POST' else [{'browserSession':[]}]
  op['parameters']=[{'name':'Origin','in':'header','required':method in ['POST','DELETE'],'schema':{'type':'string'},'description':'Exact permitted browser origin; cross-origin requests are rejected.'}]
  if method=='DELETE':op['parameters'].append({'name':'X-CSRF-Token','in':'header','required':True,'schema':{'type':'string'}})
 if method=='GET' and (path in ['/chats' ,'/deliveries','/send/schedules','/send/operations','/events'] or path.endswith(('/messages','/occurrences'))):
  op['parameters'] += [{'name':'limit','in':'query','schema':{'type':'integer','minimum':1,'maximum':100,'default':50}},{'name':'offset','in':'query','schema':{'type':'integer','minimum':0,'default':0}}]
 if method=='GET' and path in ['/send/operations','/send/schedules']:
  states=['queued','sending','succeeded','failed','unknown','cancelled'] if path=='/send/operations' else ['active','paused','completed','failed','cancelled']
  op['parameters'] += [{'name':'state','in':'query','schema':{'type':'string','enum':states}},{'name':'kind','in':'query','schema':{'type':'string','enum':['text','image','file','audio','video','voice','operation']}},{'name':'operation','in':'query','schema':{'type':'string'}},{'name':'peer','in':'query','schema':{'type':'string','pattern':'^(user|group|channel|bot|service):[^\\s]+$','maxLength':264,'description':'Scoped type:provider-ID key. IDs are validated by the selected adapter.'}}]
  op['parameters'] += [{'name':key,'in':'query','schema':date_time,'description':'Inclusive lower bound.' if key=='created_after' else 'Exclusive upper bound.'} for key in ['created_after','created_before']]
  if path=='/send/operations':op['parameters'].append({'name':'schedule_id','in':'query','schema':{'type':'string','format':'uuid'}})
 if method=='GET' and path.endswith('/occurrences'):
  op['parameters'].append({'name':'state','in':'query','schema':{'type':'string','enum':['queued','sending','succeeded','failed','unknown','cancelled']}})
  op['description']='Authoritative materialized executions of the selected schedule, with current operation results. Check the parent schedule occurrence_history_complete before assuming older history is exhaustive.'
 if method=='GET' and (path=='/events' or path.endswith('/messages')):
  op['description']='Locally persisted events only; edits and deletions remain separate events, not a reconstructed transcript. No remote history fetch. Search is case-sensitive literal Unicode substring over projected text and document captions; percent and underscore are literal. Stable time/ID ordering.'
  op['parameters'] += [{'name':'search','in':'query','schema':{'type':'string','maxLength':512},'description':'Maximum 512 UTF-8 bytes.'},{'name':'event','in':'query','schema':{'type':'string','maxLength':128},'description':'Exact event name, at most 128 UTF-8 bytes.'},{'name':'direction','in':'query','schema':{'type':'string','enum':['incoming','outgoing','unknown']}},{'name':'sender_id','in':'query','schema':filter_user_id},{'name':'media_only','in':'query','schema':{'type':'boolean'},'description':'True restricts results to projected document events.'}]
  op['parameters'] += [{'name':key,'in':'query','schema':date_time,'description':'Inclusive lower bound.' if key=='start_time' else 'Exclusive upper bound.'} for key in ['start_time','end_time']]
  if path=='/events':op['parameters'].append({'name':'peer','in':'query','schema':{'type':'string','pattern':'^(user|group|channel|bot|service):[^\\s]+$','maxLength':264,'description':'Scoped type:provider-ID key. IDs are validated by the selected adapter.'}})
 if method=='GET' and path=='/deliveries':
  op['parameters'] += [{'name':'state','in':'query','schema':{'type':'string','enum':['queued','delivering','retry','delivered','failed','paused','cancelled']}},{'name':'include_payload','in':'query','schema':{'type':'boolean','default':True},'description':'False excludes payload content from list rows. The detail endpoint still returns the full event.'}]
 schema=None
 if method=='POST' and path=='/ui/auth/session':schema={'type':'object','additionalProperties':False,'required':['username','password'],'properties':{'username':text,'password':{'type':'string','writeOnly':True}}}
 if method=='POST' and path in ['/send/'+x for x in ['message','image','file','audio','video','voice']]:
  schema=copy.deepcopy(send)
  schema['properties'].pop('kind')
  if path=='/send/message':
   for key in ['caption','ptt','media_id']:schema['properties'].pop(key)
   schema['required']=['message']
   schema['properties']['message']['minLength']=1
  else:
   schema['properties'].pop('message')
   if path!='/send/audio':schema['properties'].pop('ptt')
   schema['required']=['media_id']
   schema['description']='Use media_id from POST /media and caption for optional text. Provider format support is capability-gated.'
  if path=='/send/voice':
   op['description']='Sends a native voice note using media_id from POST /media. The upload must be single-stream Ogg Opus; GoOmni checks its bytes and derives the duration in milliseconds. Optional caption and reply_message_id use the ordinary media contract. No server-side transcoding. Scheduling uses the same durable send path.'
  op['parameters'].append({'name':'Idempotency-Key','in':'header','required':True,'schema':{'type':'string','minLength':1,'maxLength':256},'description':'Stable per-device key. Identical repeats return same operation; changed payload returns 409.'})
  op['responses']['202']={'description':'Accepted/unknown; inspect results.send_id via GET /send/operations/{send_id}. Never blindly retry unknown sends.'}
 if method=='POST' and path in mutation_bodies:
  required,properties=mutation_bodies[path]
  schema={'type':'object','required':required,'properties':properties,'additionalProperties':False}
  op['parameters'].append({'name':'Idempotency-Key','in':'header','required':True,'schema':{'type':'string','minLength':1,'maxLength':256}})
  op['responses']['202']={'description':'Durable operation pending or unknown; inspect results.send_id.'}
 if method=='GET' and path=='/message/{message_id}/download':
  op['parameters'].append({'name':'peer','in':'query','required':True,'schema':{'type':'string','pattern':'^(user|group|channel|bot|service):[^\\s]+$','maxLength':264},'description':'Account-scoped peer from a received message. Provider access hashes are never accepted.'})
 if method=='GET' and path=='/chats':
  op['parameters'].append({'name':'source','in':'query','schema':{'type':'string','enum':['local','remote'],'default':'local'}})
  op['parameters'].append({'name':'date','in':'query','schema':{'type':'string','pattern':'^[0-9]+$'},'description':'Remote mode only: millisecond cursor. Omit for the newest dialogs (native sentinel -1). Explicit "0" means the Unix epoch; it is not the latest-page default. Use results.next_date for subsequent pages.'})
  for parameter in op['parameters']:
   if parameter['name']=='limit':
    parameter['schema'].pop('default',None)
    parameter['description']='Defaults to 50 in local mode and 20 in remote mode; remote bounds are 1..100.'
   if parameter['name']=='offset':parameter['description']='Local mode only. Remote pagination uses date; offset is ignored.'
 if method=='GET' and path.endswith('/history'):
  op['parameters'] += [{'name':'limit','in':'query','schema':{'type':'integer','minimum':1,'maximum':100,'default':20}}, {'name':'date','in':'query','schema':{'type':'string','pattern':'^[0-9]+$'},'description':'Millisecond anchor: backward mode 2 defaults to the current time; forward mode 1 defaults to zero (Unix epoch); around mode 3 requires an explicit date. In directional modes use results.next_date to continue; stop when incomplete=true and pagination_stop_reason=non_advancing_cursor. Deduplicate message IDs at date boundaries; exhaustive export is unproven. Around mode returns before_date/after_date bounds, without claiming more history exists.'}, {'name':'load_mode','in':'query','schema':{'type':'integer','enum':[1,2,3],'default':2},'description':'1: forward; 2: backward; 3: both directions around an explicit date anchor.'}]
 if method=='GET' and path=='/app/capabilities':
  op['x-goomni-provider-catalogs']=provider_catalogs
  op['parameters'].append({'name':'provider','in':'query','required':True,'schema':provider_id,'description':'Explicit messenger; absent or invalid selectors never fall back to Bale.'})
 if method=='PATCH' and path.endswith('/webhook'):schema={'$ref':'#/components/schemas/WebhookPatch'}
 if method=='POST' and path.endswith('/login'):schema={'$ref':'#/components/schemas/Login'}
 if method=='POST' and path.endswith('/login/code'):schema={'$ref':'#/components/schemas/Code'}
 if method=='POST' and path.endswith('/login/password'):schema={'$ref':'#/components/schemas/Password'}
 if method=='POST' and path=='/devices':schema={'type':'object','additionalProperties':False,'required':['device_id','provider'],'properties':{'provider':provider_id,'device_id':{'type':'string','minLength':1,'maxLength':64,'pattern':'^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$','description':'1-64 ASCII characters; begin with a letter or digit, followed by letters, digits, dots, underscores or hyphens.'},**schemas['WebhookPatch']['properties']}}
 if method=='POST' and path=='/devices':
  op['parameters'].append({'name':'Idempotency-Key','in':'header','required':True,'schema':{'type':'string','minLength':1,'maxLength':256},'description':'Persist a unique provisioning key and the exact initial configuration before calling. Identical retries return the same immutable connection (200), changed requests conflict (409). This gateway-wide namespace is separate from per-connection send keys. Retrying a deleted connection returns PROVISIONING_RETIRED; never attaches a reused alias.'})
  op['description']='Atomically persists the new connection, initial webhook configuration and provisioning key. 201 creates; 200 replays without resetting current configuration. Secrets are write-only. Consumers own user/channel authorization.'
 if method=='POST' and path=='/send/schedules':schema={'allOf':[{'$ref':'#/components/schemas/ScheduleRequest'}],'description':'Creates a schedule, not an immediate operation. A required Idempotency-Key returns the original schedule for identical repeats, including after its due time; changed content conflicts.'}
 if method=='POST' and path=='/send/schedules':
  op['parameters'].append({'name':'Idempotency-Key','in':'header','required':True,'schema':{'type':'string','minLength':1,'maxLength':256},'description':'Stable key shared with scheduled /send/{kind} and immediate sends for this connection. Matching retries return the original schedule; changed payload returns409.'})
 if method=='POST' and path=='/media/fetch':schema={'type':'object','required':['url'],'properties':{'url':{'type':'string','format':'uri'},'name':{'type':'string'}}}
 if extension:
  op['description']=extension['description']+' Verification: '+extension['verification']+'. The same schema can be sent as JSON to POST /operations/'+extension['operation']+'.'
  op['x-goomni-operation']=extension['operation']
  if method=='POST':
   schema=json.loads(json.dumps(extension['request']))
   for name in re.findall(r'{([^}]+)}',path):
    schema['properties'].pop(name,None)
    if name in schema.get('required',[]):schema['required'].remove(name)
  else:
   for name,field in extension['request']['properties'].items():
    parameter={'name':name,'in':'query','required':name in extension['request'].get('required',[])}
    if field['type']=='object' and 'id' in field.get('properties',{}):parameter['schema']={'type':'string','pattern':'^(user|group|channel|bot|service):[^\\s]+$','maxLength':264}
    elif field['type'] in ('object','array'):parameter['content']={'application/json':{'schema':field}}
    else:parameter['schema']=field
    op['parameters'].append(parameter)
  if extension['mode']=='mutation':op['parameters'].append({'name':'Idempotency-Key','in':'header','required':True,'schema':{'type':'string','minLength':1,'maxLength':256}})
 if method=='POST' and path=='/operations/{operation}':
  schema={'anyOf':[entry['request'] for entry in all_provider_operations],'description':'The selected provider and path operation selects exactly one catalog contract. anyOf is intentional: different operations may have identical request shapes.'}
  op['description']='Validated named native operation. Discover operation names, schemas and mode with GET /app/capabilities?provider=<provider> or the selected device capabilities route. Mutations require Idempotency-Key and return an Operation (200 acknowledged, 202 pending/unknown); reads and ephemeral presence return normalized operation results. No arbitrary service/method RPC proxy.'
  for parameter in op['parameters']:
   if parameter['name']=='operation':parameter['schema']['enum']=all_operation_names
  op['parameters'].append({'name':'Idempotency-Key','in':'header','schema':{'type':'string','minLength':1,'maxLength':256},'description':'Required for mutation mode.'})
  op['responses']['202']=response('OperationResponse','Mutation pending or unknown')
  op['responses']['422']=response('OperationResponse','Mutation failed')
 if method=='POST' and path=='/media':
  op['requestBody']={'required':True,'content':{mime:{'schema':{'type':'string','format':'binary'}} for mime in ['application/octet-stream','audio/ogg','application/ogg','audio/opus']},'description':'Stores bytes in the selected device media namespace. Set the actual Content-Type and X-Filename; the listed Ogg types support native voice. Other media types are accepted and validated by their later send operation. Local upload success does not establish provider format support.'}
  op['parameters'].append({'name':'X-Filename','in':'header','schema':{'type':'string'}})
 elif schema:op['requestBody']={'required':True,'content':{'application/json':{'schema':schema}}}
 elif method in ['POST','PUT','PATCH'] and path not in ['/devices/{device_id}/logout','/devices/{device_id}/reconnect'] and not path.endswith(('/pause','/resume','/cancel','/retry','/replay')):op['requestBody']={'content':{'application/json':{'schema':{'type':'object','additionalProperties':True}}},'description':'Provider-specific operation. See GET /app/capabilities and the operation description; unsupported operations return 501.'}
 if method=='POST' and path in ['/send/'+kind for kind in ['file','image','audio','video','voice']]:
  form=copy.deepcopy(schema['properties'])
  form.pop('media_id')
  file_field=path.rsplit('/',1)[-1]
  if file_field=='voice':file_field='audio'
  form[file_field]={'type':'string','format':'binary','description':'Exactly one file within the configured media byte limit; UTF-8 filename at most 255 bytes; actual Content-Type (defaults to application/octet-stream).'}
  op['requestBody']['content']['multipart/form-data']={'schema':{'type':'object','additionalProperties':False,'required':[file_field],'oneOf':[{'required':['peer']},{'required':['phone']}],'properties':form},'encoding':{key:{'contentType':'application/json'} for key in ['peer','mentions','weekdays']}}
  op['description']=op.get('description','')+' Multipart uses ordinary fields plus the endpoint-named file part (voice uses audio). Choose phone or a peer JSON object; mentions/weekdays are JSON arrays; ptt is true/false. Combined form metadata is bounded to 128 KiB and 20 parts. scheduled_at creates a schedule with its file in one durable transaction; immediate sends register the file and outbox request together. Keyed retries bind file SHA256, filename, MIME and canonical request; changed content conflicts. Scheduling and immediate sends share the connection idempotency namespace. Unused retry/conflict files are cleaned. No audio conversion, compression or WhatsApp-only flags.'
  if path=='/send/image':op['description']+=' Image header decoding accepts JPEG, PNG, GIF and WebP, bounded to 8192 pixels per side and 16,777,216 pixels total. WebP native receipt/rendering remains live-unverified.'
 if method=='POST' and path in ['/devices','/media','/media/fetch']:
  op['responses'].pop('200',None)
  op['responses']['201']={'description':'Created','content':{'application/json':{'schema':{'$ref':'#/components/schemas/Envelope'}}}}
 if path=='/metrics':op['responses']['200']={'description':'Prometheus text metrics','content':{'text/plain':{'schema':{'type':'string'}}}}
 if path in ['/media/{media_id}','/message/{message_id}/download','/user/avatar']:op['responses']['200']={'description':'Authenticated, device-scoped media stream','content':{'application/octet-stream':{'schema':{'type':'string','format':'binary'}}}}
 if method=='GET' and path=='/user/avatar':
  op['description']='Downloads the selected account-visible avatar for a user peer as authenticated image bytes after bounded download, header validation and image decoding (GIF first frame only). Maximum dimensions are 8192 pixels per side and 16,777,216 total pixels. The requested rendition is preferred; a different available rendition is used when it is absent. Provider locations and access hashes stay internal. No arbitrary URL input.'
  op['responses']['200']={'description':'Validated avatar image bytes, bounded by 8 MiB and the configured media limit, 8192 pixels per side and 16,777,216 total pixels. GIF validation covers its first frame only. No JSON envelope.', 'content':{mime:{'schema':{'type':'string','format':'binary'}} for mime in ['image/jpeg','image/png','image/gif']}}
  op['responses']['404']['description']='AVATAR_NOT_FOUND when an avatar is absent or hidden; peer lookup may also return PEER_NOT_FOUND.'
  op['responses']['502']={'description':'AVATAR_INVALID for malformed provider image data, or another bounded provider/download error.'}
  op['parameters'] += [{'name':'peer','in':'query','required':True,'schema':{'type':'string','pattern':'^user:[^\\s]+$','maxLength':261},'description':'Provider user ID scoped to the selected account, including self; adapter-specific format validation applies.'},{'name':'size','in':'query','schema':{'type':'string','enum':['small','large'],'default':'small'}}]
 if method=='GET' and path in ['/group/participants','/group/info','/group/invite-link']:
  op['parameters'].append({'name':'peer','in':'query','required':True,'schema':{'type':'string','pattern':'^(group|channel):[^\\s]+$','maxLength':264},'description':'Group ID scoped to the selected account; access references are resolved internally.'})
 if method=='GET' and path=='/group/participants':
  op['parameters'] += [{'name':'limit','in':'query','schema':{'type':'integer','minimum':1,'maximum':100,'default':20}},{'name':'next','in':'query','schema':{'type':'string'},'description':'Opaque cursor returned by the previous page.'}]
 if method=='GET' and path=='/user/my/groups':
  op['parameters'].append({'name':'is_owner','in':'query','schema':{'type':'boolean','default':False}})
 if method=='GET' and path=='/user/search':
  op['parameters'].append({'name':'query','in':'query','required':True,'schema':{'type':'string'}})
 if method=='GET' and path=='/user/info':
  op['description']='Returns the selected account own profile only. Arbitrary user lookup is not supported.'
 if method=='POST' and (path in mutation_bodies or path.startswith('/send/')):
  op['responses']['422']={'description':'Persisted operation failed or was cancelled. Inspect results and error code; no automatic retry of ambiguous work.'}
 if path.startswith('/media') or path.endswith('/download') or path=='/user/avatar':
  op['responses']['413']={'description':'AVATAR_TOO_LARGE: avatar exceeds 8 MiB or the configured media limit' if path=='/user/avatar' else 'Media exceeds configured limit'}
  op['responses']['503']={'description':'Transfer capacity exhausted'}
 if method in ['POST','PATCH','PUT']:
  op['responses']['413']={'description':'Request exceeds configured body or media limit'}
 # Every concrete JSON success gets its actual result model. Unsupported routes
 # advertise errors only, not fabricated successful payloads.
 model=success_models.get((method,path))
 if model:
  status='201' if method=='POST' and path in ['/devices','/media','/media/fetch'] else '200'
  op['responses'][status]=response(model)
  if method=='POST' and path=='/devices':op['responses']['200']=response(model,'Existing immutable connection from an identical provisioning retry')
 elif path in unsupported_paths:
  op['responses'].pop('200',None)
  op['responses'].pop('202',None)
  op['responses'].pop('422',None)
  op['description']='This compatibility route currently returns FEATURE_NOT_SUPPORTED (501). No successful provider contract is claimed.'
 elif path not in ['/metrics','/media/{media_id}','/message/{message_id}/download','/user/avatar']:
  raise RuntimeError('Missing concrete success model: '+method+' '+path)
 if model in ['SendOrScheduleResponse','OperationResponse'] and method=='POST':
  op['responses']['202']=response('OperationResponse','Queued, sending or unknown. Inspect results.send_id; never blindly resend unknown work.')
  op['responses']['422']=response('OperationResponse','Persisted operation failed or was cancelled.')
  for status in ['409','501']:
   op['responses'][status]['content']={'application/json':{'schema':{'oneOf':[ref('ErrorResponse'),ref('OperationResponse')]}}}
 elif model=='NativeOperationResponse':
  for status in ['409','501']:
   op['responses'][status]['content']={'application/json':{'schema':{'anyOf':[ref('ErrorResponse'),ref('OperationResponse')]}}}
 elif method=='POST' and path.startswith('/send/'):
  op['responses'].pop('422',None)
 for status,body in op['responses'].items():
  if int(status)>=400 and 'content' not in body:body['content']={'application/json':{'schema':ref('ErrorResponse')}}
 op['responses']['default']=response('ErrorResponse','Other error; code and message describe the failure. Secrets are never returned.')
 if path!='/health':
  for body in op['responses'].values():
   body['headers']={'Cache-Control':{'schema':{'type':'string','enum':['no-store']},'description':'Authenticated responses must not be cached; some native results contain short-lived Mini App launch credentials.'},'X-Content-Type-Options':{'schema':{'type':'string','enum':['nosniff']}}}
 native_operation=route_operations.get((method,path))
 if native_operation:
  selected={provider:next((entry for entry in entries if entry['operation']==native_operation),None) for provider,entries in provider_catalogs.items()}
  selected={provider:entry for provider,entry in selected.items() if entry is not None}
  if selected:
   op['x-goomni-providers']=list(selected)
   op['x-goomni-operation']=native_operation
   requests={}
   for provider,entry in selected.items():
    request=copy.deepcopy(entry['request'])
    path_fields=set(re.findall(r'{([^}]+)}',path))
    if 'chat_jid' in path_fields:path_fields.add('peer')
    for field in path_fields:
     request.get('properties',{}).pop(field,None)
     if field in request.get('required',[]):request['required'].remove(field)
    requests[provider]=request
   op['x-goomni-provider-requests']=requests
   op['description']='Provider operation '+native_operation+'. The selected immutable connection chooses its request contract, pagination and result projection. Supported providers: '+', '.join(selected)+'. See x-goomni-provider-requests and the capability discovery response. '+('source=local returns local event aggregates; source=remote uses this native contract. ' if path=='/chats' else '')+op.get('description','')
   if method in ['POST','PUT','PATCH']:
    op['requestBody']={'required':True,'content':{'application/json':{'schema':{'anyOf':list(requests.values()),'description':'Select the schema for the connection provider; identical provider shapes intentionally overlap.'}}}}
   elif method=='GET':
    preserved=[p for p in op['parameters'] if p['in']!='query' or p['name'] in ['device_id','source'] or (path=='/chats' and p['name']=='offset')]
    query_names=sorted(set().union(*(set(request.get('properties',{})) for request in requests.values())))
    for name in query_names:
     choices={provider:request['properties'][name] for provider,request in requests.items() if name in request.get('properties',{})}
     fields=list(choices.values());field=fields[0]
     parameter={'name':name,'in':'query','required':all(name in request.get('required',[]) for request in requests.values()),'description':'Provider-specific field for '+', '.join(choices)+'.','x-goomni-provider-schemas':choices}
     if field['type']=='object' and 'id' in field.get('properties',{}):parameter['schema']={'type':'string','pattern':'^(user|group|channel|bot|service):[^\\s]+$','maxLength':264}
     elif field['type'] in ['object','array']:parameter['content']={'application/json':{'schema':{'anyOf':fields}}}
     else:parameter['schema']={'anyOf':fields}
     preserved.append(parameter)
    op['parameters']=preserved
    if any(provider!='bale' for provider in selected):
     original=op['responses']['200']['content']['application/json']['schema']
     op['x-goomni-provider-results']={provider:(original if provider=='bale' else ref('NativeResultResponse')) for provider in selected}
     op['responses']['200']['content']['application/json']['schema']={'anyOf':[original,ref('NativeResultResponse')]}
     op['responses']['200']['description']='Bale retains the documented Bale projection. Other providers return their reviewed native projection; fields and cursor formats are provider-specific.'
 paths.setdefault(path,{})[method.lower()]=op
core_summaries={
 ('GET','/events'):'Search locally persisted events',
 ('GET','/send/operations'):'List durable send operations',
 ('GET','/send/schedules/{schedule_id}/occurrences'):'List materialized schedule executions',
 ('GET','/devices/overview'):'Get local device and delivery status snapshots',
 ('GET','/devices/{device_id}/login'):'Resume pending login metadata',
 ('GET','/ui/auth/session'):'Read the administrative browser session',
 ('POST','/ui/auth/session'):'Create an administrative browser session',
 ('DELETE','/ui/auth/session'):'Log out the administrative browser session',
 ('GET','/health'):'Check service liveness',
 ('GET','/ready'):'Check storage readiness',
 ('GET','/metrics'):'Get Prometheus metrics',
 ('GET','/app/providers'):'List messenger availability and verification',
 ('GET','/devices/{device_id}/capabilities'):'List capabilities for the selected connection',
 ('GET','/app/info'):'Get service version and capabilities',
 ('GET','/app/capabilities'):'List typed operation contracts',
 ('GET','/app/status'):'Get account connection status',
 ('GET','/app/devices'):'List devices',
 ('GET','/devices'):'List devices',
 ('POST','/devices'):'Create a device',
 ('GET','/devices/{device_id}'):'Get a device',
 ('DELETE','/devices/{device_id}'):'Delete a device',
 ('GET','/devices/{device_id}/status'):'Get device connection status',
 ('POST','/devices/{device_id}/login'):'Start phone login',
 ('POST','/devices/{device_id}/login/code'):'Submit a login code',
 ('POST','/devices/{device_id}/login/password'):'Submit a two-step password',
 ('POST','/devices/{device_id}/reconnect'):'Reconnect a device',
 ('POST','/devices/{device_id}/logout'):'Log out a device',
 ('GET','/devices/{device_id}/webhook'):'Get device webhook settings',
 ('PATCH','/devices/{device_id}/webhook'):'Update device webhook settings',
 ('GET','/deliveries'):'List webhook deliveries',
 ('GET','/deliveries/{delivery_id}'):'Get a webhook delivery',
 ('POST','/deliveries/{delivery_id}/retry'):'Retry a failed webhook delivery',
 ('POST','/deliveries/{delivery_id}/replay'):'Replay an event to current destinations',
 ('GET','/send/operations/{send_id}'):'Get a send operation',
 ('GET','/send/schedules'):'List message schedules',
 ('POST','/send/schedules'):'Create a message schedule',
 ('GET','/send/schedules/{schedule_id}'):'Get a message schedule',
 ('POST','/send/schedules/{schedule_id}/pause'):'Pause a message schedule',
 ('POST','/send/schedules/{schedule_id}/resume'):'Resume a message schedule',
 ('POST','/send/schedules/{schedule_id}/cancel'):'Cancel a message schedule',
 ('POST','/send/message'):'Send a text message',
 ('POST','/send/image'):'Send an image',
 ('POST','/send/file'):'Send a file',
 ('POST','/send/audio'):'Send an audio file',
 ('POST','/send/video'):'Send a video',
 ('POST','/send/voice'):'Send a native voice note',
 ('POST','/media'):'Upload media to a device',
 ('POST','/media/fetch'):'Fetch media from a public URL',
 ('GET','/media/{media_id}'):'Download a local media file',
 ('GET','/message/{message_id}/download'):'Download a message attachment',
 ('GET','/chats'):'List conversations',
 ('GET','/chat/{chat_jid}/history'):'Load provider-specific conversation history',
 ('GET','/chat/{chat_jid}/messages'):'List stored conversation events',
 ('POST','/message/{message_id}/update'):'Edit a text message',
 ('POST','/message/{message_id}/delete'):'Delete a message',
 ('POST','/message/{message_id}/forward'):'Forward a message',
 ('POST','/message/{message_id}/read'):'Mark messages read through a timestamp',
 ('POST','/message/{message_id}/revoke'):'Revoke a message (not supported)',
 ('GET','/user/info'):'Get the connected account profile',
 ('GET','/user/avatar'):'Download a contact avatar',
 ('GET','/user/my/contacts'):'List contacts',
 ('GET','/user/search'):'Search contacts',
 ('GET','/user/my/groups'):'List joined groups',
 ('POST','/group'):'Create a group',
 ('GET','/group/info'):'Get group details',
 ('GET','/group/invite-link'):'Get a group invite link',
 ('GET','/group/participants'):'List group members',
 ('POST','/group/participants'):'Invite users to a group',
 ('POST','/group/participants/remove'):'Remove a group member',
 ('POST','/group/name'):'Change a group name',
 ('POST','/group/topic'):'Change a group description',
 ('POST','/operations/{operation}'):'Run a typed operation',
}
native_summaries={
 'users.get':'Get user profiles',
 'account.username.check':'Check username availability',
 'account.blocked':'List blocked users',
 'account.sessions':'List account sessions',
 'account.settings':'Get account settings',
 'account.privacy':'Get privacy rules',
 'account.privacy.status':'Get privacy status',
 'account.name':'Change the account display name',
 'account.about':'Change the account bio',
 'account.username':'Change the account username',
 'account.avatar':'Change the account avatar',
 'account.block':'Block a user',
 'account.unblock':'Unblock a user',
 'account.privacy.set':'Update a privacy rule',
 'account.settings.set':'Update an account setting',
 'account.session.terminate':'Terminate an account session',
 'account.sessions.terminate':'Terminate other account sessions',
 'contacts.add':'Add a contact',
 'contacts.remove':'Remove a contact',
 'contacts.rename':'Rename a contact',
 'contacts.import':'Import phone contacts',
 'contacts.reset':'Reset imported contacts',
 'contacts.resolve':'Resolve a phone number to a user',
 'group.permissions':'Get member permissions',
 'group.default_permissions':'Get default group permissions',
 'group.permissions.set':'Update member permissions',
 'group.default_permissions.set':'Update default group permissions',
 'group.admins':'List group administrators',
 'group.banned':'List banned group members',
 'group.pins':'List pinned group messages',
 'group.preview':'Preview a group invitation',
 'group.promote':'Promote a group member',
 'group.demote':'Demote a group administrator',
 'group.transfer':'Transfer group ownership',
 'group.unban':'Unban a group member',
 'group.join':'Join a group by invitation',
 'group.join_public':'Join a public group',
 'group.leave':'Leave a group',
 'group.link.revoke':'Revoke a group invite link',
 'group.unpin_all':'Unpin all group messages',
 'group.username':'Change a group username',
 'group.restriction':'Update group restrictions',
 'group.history':'Change history visibility for new members',
 'group.visibility':'Change group member visibility',
 'group.pin':'Pin a group message',
 'group.unpin':'Unpin a group message',
 'group.photo':'Change a group photo',
 'group.photo.remove':'Remove a group photo',
 'channel.create':'Create a channel',
 'channel.list':'List channels',
 'chat.clear':'Clear conversation history',
 'chat.delete':'Delete a conversation',
 'message.received':'Acknowledge received messages',
 'message.pin':'Pin a message',
 'message.unpin':'Unpin a message',
 'message.unpin_all':'Unpin all conversation messages',
 'message.pins':'List pinned messages',
 'message.reactions':'Get message reactions',
 'message.views':'Get message view counts',
 'message.views.increment':'Increment message view counts',
 'message.reaction.set':'Set a message reaction',
 'message.reaction.remove':'Remove a message reaction',
 'message.reaction.users':'List users for a reaction',
 'message.upvoters':'List message upvoters',
 'message.upvote':'Upvote a message',
 'message.upvote.remove':'Remove a message upvote',
 'folders.list':'List conversation folders',
 'folders.create':'Create a conversation folder',
 'folders.edit':'Update a conversation folder',
 'folders.delete':'Delete a conversation folder',
 'presence.online':'Set account online presence',
 'presence.typing':'Send a typing or activity signal',
 'presence.stop':'Stop a typing or activity signal',
 'presence.users':'Get user presence',
 'presence.contacts':'Get contact presence',
 'presence.group':'Get group presence',
 'presence.group.count':'Get the group online count',
 'send.poll':'Create and send a poll',
 'poll.vote':'Submit or retract a poll vote',
 'poll.close':'Close a poll',
 'poll.results':'Get poll results',
 'poll.full_results':'Get detailed poll results',
 'sticker.list':'List sticker collections',
 'sticker.get':'Get a sticker collection',
 'sticker.pack.add':'Add a sticker collection',
 'sticker.pack.remove':'Remove a sticker collection',
 'send.sticker':'Send a sticker',
 'report.peer':'Report a user or group',
 'report.messages':'Report messages',
 'report.dismiss':'Dismiss a report prompt',
 'report.story':'Report stories',
 'miniapp.url':'Get a signed Mini App launch URL',
 'miniapp.hash':'Get Mini App authentication data',
 'miniapp.menu':'Get a Mini App menu',
 'miniapp.data':'Send Mini App data',
 'miniapp.custom':'Invoke a Mini App method',
 'bot.callback':'Send a bot button callback',
 'link.summary':'Get a link preview',
 'story.list':'List the available story feed',
 'story.get':'Get a story',
 'story.viewers':'List story viewers',
 'story.add':'Publish a text or image story',
 'story.delete':'Delete a story',
 'story.react':'React to a story',
 'wallet.list':'List wallet balances',
 'wallet.kifpools':'Get legacy wallet balance information',
 'send.contact':'Send a contact card',
 'send.location':'Send a location',
 'send.template':'Send a keyboard template (experimental)',
}
def operation_summary(method,path,native_operation=None):
 if native_operation:
  if native_operation not in native_summaries:
   if (method,path) in core_summaries:return core_summaries[(method,path)]
   raise RuntimeError('Missing human summary for native operation: '+native_operation)
  return native_summaries[native_operation]
 if (method,path) not in core_summaries:raise RuntimeError('Missing human summary for route: '+method+' '+path)
 return core_summaries[(method,path)]
def route_tag(method,path,native_operation=None):
 if path=='/events':return 'Conversations'
 if path.startswith('/deliveries'):return 'Webhooks'
 if path in ['/app/devices','/app/status']:return 'Devices and login'
 if native_operation and native_operation.startswith('presence.'):return 'Presence'
 if path.startswith('/presence'):return 'Presence'
 if path.startswith('/report'):return 'Reports'
 if path.startswith('/bot'):return 'Bot callbacks'
 if path.startswith('/link'):return 'Link previews'
 if path=='/user/my/groups':return 'Groups'
 if path=='/user/avatar':return 'Contacts' if method=='GET' else 'Account'
 if path.startswith('/user/contacts') or path in ['/user/my/contacts','/user/search']:return 'Contacts'
 if '/webhook' in path:return 'Webhooks'
 if path.startswith('/send/schedules'):return 'Schedules'
 if path.startswith('/send/operations'):return 'Send operations'
 if path.startswith('/send/'):return 'Send messages'
 if path.startswith('/media') or path.endswith('/download'):return 'Media'
 if path.startswith('/devices'):return 'Devices and login'
 if path.startswith('/message'):return 'Message actions'
 if path.startswith('/chat'):return 'Conversations'
 if path.startswith('/group'):return 'Groups'
 if path.startswith('/channel'):return 'Channels'
 if path.startswith('/contact') or path in ['/user/profiles','/user/check']:return 'Contacts'
 if path.startswith('/user'):return 'Account'
 if path.startswith('/folder'):return 'Folders'
 if path.startswith('/sticker'):return 'Stickers'
 if path.startswith('/stor'):return 'Stories'
 if path.startswith('/mini'):return 'Mini Apps'
 if path.startswith('/wallet'):return 'Wallet information'
 if path.startswith('/poll'):return 'Polls'
 return 'Service'
for path,operations in paths.items():
 for method,operation in operations.items():
  native_operation=operation.get('x-goomni-operation')
  operation['summary']=operation_summary(method.upper(),path,native_operation)
  operation['tags']=[route_tag(method.upper(),path,native_operation)]
# These notes carry consumer-facing constraints formerly split across feature
# guides. Keep field shapes in the shared domain catalog; prose belongs here.
tag_notes={
 'Devices and login': '''Each alias owns an immutable connection. An explicit invalid selector never falls back to another account. Authentication, transport and recovery are separate states: a connected socket does not establish completed recovery. A device previously bound to one account cannot be rebound to a different account. Deleting and reusing an alias does not transfer its old jobs. OTPs/passwords belong only in request bodies. Login challenges expose safe string delivery methods, an independent resend cooldown and expiry bounded by ten minutes. Unknown delivery methods are reported as unknown without leaking wire data. GoOmni never silently resends a code; a new login request creates a new challenge. An awaiting_password challenge requires the password endpoint.''',
 'Send messages': '''All sends persist before provider I/O. The selected account and payload define an Idempotency-Key: an identical repeat returns its existing operation; changed content conflicts. HTTP 200 means provider acknowledgment, not recipient delivery or reading. Requests wait up to the configured send wait (default 40 seconds); 202 returns pending/unknown work to inspect through /send/operations/{send_id}. Do not blindly retry unknown results. Provider request deduplication is not implied by the local journal ID. Text and captions have a 65,536-byte UTF-8 limit. Supply peer or phone, never both; exact phone lookup does not implicitly import a contact or prove an inaccessible number unregistered.''',
 'Send operations': '''Inspect results.state before using results.message_id or results.data. Acknowledged sends expose message_id/date directly in results; extension mutations put normalized output in results.data. An exact authenticated own-message echo can reconcile the message ID and date. Pending and unknown results never imply message acknowledgement. An unknown outcome must be reconciled, not blindly resubmitted. Reads and ephemeral presence calls are not outbox work and return read errors on timeout.''',
 'Schedules': '''Schedules are local durable jobs, not provider-side tasks imported from another client. Timezone is an IANA name. A due occurrence creates at most one durable send despite restart. Ordinary text/media/voice the rich producers send.poll, send.sticker, send.contact, send.location and send.template, and message.forward are supported. Forward stores a source reference and resolves it at execution; administrative changes and stories are not. Rich fields belong in payload, including peer. A media asset remains pinned while a schedule or unresolved send needs it. Pause, resume and cancel affect future occurrences; they cannot undo an acknowledged send.''',
 'Media': '''Upload bytes first and use the returned account-scoped media_id. Upload success establishes local storage, not provider support for that format. Ordinary send media is not transcoded. Rubika avatar changes separately derive the observed 200/800 square JPEG renditions from an inspected image. Downloads require administrative authentication and the same selected account; provider references remain internal. Fetching external media is bounded and rejects internal network addresses and untrusted redirects. Nested received documents use the same scoped download API; quoted content does not attach someone else's media to the new message.''',
 'Account': '''Account operations affect the selected account. Privacy type 0 is invitations, 1 presence and 2 money transfer; status 0 everyone, 1 contacts and 2 nobody. This is the reviewed legacy privacy API, not a separate provider configuration catalog. IDs and provider timestamps are decimal strings. Settings preserve an empty string and remove a key on null; credential-like keys are refused on write and redacted on read. Session lists omit credentials, authentication-holder values, IP addresses and location. A session must appear in this account's current session list before terminating it. Account signup/deletion, phone changes and two-factor configuration/recovery are not available through these generic operations.''',
 'Contacts': '''Every peer is resolved within the selected account. Exact phone matching uses actual provider contact information and never picks the first fuzzy result: PHONE_NOT_FOUND means no accessible exact match; PHONE_AMBIGUOUS means multiple distinct matches. Import can return fewer contacts than requested and does not prove that omitted numbers are unregistered. Access hashes are neither accepted nor returned. users.get with full=true returns supplementary about/language/timezone/flag metadata, while ordinary users include names/usernames; these are distinct provider projections.''',
 'Presence': '''Presence calls are bounded, explicit and ephemeral: no subscriptions, automatic renewal or replay after restart. An online signal timeout defaults to 90,000 milliseconds. Typing types: 1 text, 2 recording voice, 3 sending voice, 4 file, 5 photo, 6 video, 7 music, 8 selecting sticker, 9 selecting GIF, 10 creating gift packet, 11 album, 12 selecting emoji. Last-seen privacy and bucket metadata remain provider-controlled.''',
 'Message actions': '''Preserve original nonzero signed message IDs, including negative IDs, as decimal strings. Message positions also require the original millisecond date and sometimes a sequence; do not invent the current time as their position. Counts remain decimal strings and absent counts are omitted, not fabricated as zero. Reading view counts does not increment them; incrementing is a separate explicit mutation. Reactions, voter lists and upvotes are subject to provider visibility and permission limits. Upvoter next cursors are unpadded base64url provider bytes, bounded to 4096 decoded bytes.''',
 'Groups': '''Group/channel references remain private to the selected account. Permissions use patch semantics by default: omitted and unknown provider fields are retained by read/merge/write. replace requires all 20 permission booleans. Calls from this process are serialized per account; another official client can still race with this operation. History sharing is a separate setting from the see_message permission. Group creation may report requested users that were not added: do not infer membership without checking. Pins need their original sender/message/date. Images use a selected-account media_id; no arbitrary external URL or access hash is accepted.''',
 'Channels': '''Channel permissions and visibility remain provider-controlled. Preserve peer.type=channel when returned; do not coerce it to a group or user. Group administration contracts apply where a route accepts group/channel peers. Neither route presence nor source-level tests prove every account/permission combination works live.''',
 'Conversations': '''Clear/delete require confirm:true and affect the selected account's view. Local stored events and provider history are distinct sources with their documented pagination. Original message positions remain strings. A reply/forward may contain quoted_message, and templates nest content: render those typed structures instead of assuming top-level text. Unsupported content and update variants stay explicit; encrypted-private messages are unsupported.''',
 'Polls': '''Poll IDs are nonzero signed int64 decimal strings. New option IDs are zero-based array positions. Question length is 1–300 Unicode characters, with 2–10 options of 1–100 characters; anonymous and multiple default false. A vote requires 1–10 distinct nonnegative int32 option IDs; retraction uses retract:true with an empty option_ids array. Detailed results honor anonymity and permissions. Full results are bounded to 100 options and 10,000 voters; the provider RPC has no reviewed pagination, so larger responses fail rather than silently truncate.''',
 'Stickers': '''Collection/sticker IDs are positive int32 decimal strings. Inventory contains installed packs of this account, not a public catalog. Follow the opaque next cursor until empty. Collection resolution visits at most 16 pages: STICKER_COLLECTION_NOT_FOUND means absent; STICKER_LOOKUP_LIMIT means the bounded search could not finish. It never borrows another account's references. Use the animated value from the chosen inventory entry: true selects the legacy animated descriptor rather than a general format flag. Normal descriptors include image, Lottie and WebM formats. Installing a pack and sending are separate journaled operations; custom uploads, pack editing and arbitrary external sticker URLs are unsupported.''',
 'Stories': '''Story IDs are opaque strings. Listing is bounded to 5000 user stories and is not a complete global inventory. Unsupported channel/bot variants return FEATURE_NOT_SUPPORTED. Returned image/video metadata does not establish downloadable story media (download_supported=false). Text or PNG/JPEG/GIF image publishing is available; video upload, widgets, overlays and editing are unverified. Story mutations have no reviewed provider request-ID deduplication; an uncertain result is not retried blindly. Listing/getting a story does not count as viewing it: view is an explicit reaction. These story mutations have offline tests, not a live publication claim.''',
 'Mini Apps': '''Launch URL/hash results are provider-issued short-lived credentials. Responses are no-store; never log, persist in analytics or share returned authentication data. There is no unsigned local fallback. Custom/data methods carry app-specific data for the intended bot, not account-login or card-payment credentials. The standalone Go helper package is github.com/mimalef70/goomni/src/pkg/miniapp; signing helpers are not proof that a Bale user logged in. Applications remain responsible for replay prevention and must not treat ParseUnverified as authentication.''',
 'Bot callbacks': '''Use the intended bot and the original signed message ID plus millisecond date for a callback. Acknowledgment does not imply a Bot API callback-query workflow or successful application action. An uncertain mutation is never automatically replayed. Received buttons are metadata and never execute callbacks or links on their own.''',
 'Reports': '''Reports are explicit journaled mutations, not local filtering. Kinds: 1 scam, 2 inappropriate content, 3 other, 4 violence, 5 spam, 6 false information. Message targets require original IDs and timestamps. Story reports accept 1–100 distinct opaque IDs (up to 512 UTF-8 bytes each, without control characters) and a description of up to 1024 Unicode characters. Reports remain subject to provider rules; an unknown acknowledgment is not blindly retried.''',
 'Wallet information': '''Read-only sanitized balances: no payment, transfer, charge, claim or withdrawal. Payment tokens, card/account numbers, authenticated wallet links and personal first/last names are omitted. Amounts are signed int64 decimal strings. Modern currency 0 means rial and 1 score; unknown nonnegative currency values are preserved. No unit is inferred for legacy kifpool balances. Output is bounded to 100 wallets and 16 balances per wallet.''',
 'Webhooks': '''Events persist before recovery checkpoints, and each selected destination has its own durable delivery. Delivery is at-least-once: deduplicate event_id and return success only after accepting the event. session_id is the local device alias; device_id is the provider account ID; provider identifies the messenger; instance_id identifies the immutable connection on newly persisted events. Configure an override per device or inherit global destinations. A URL change pauses unstarted deliveries to the previous configuration; explicit replay selects the new destination. A secret-only change for the same URL applies on the next attempt. Secrets are write-only. See the [webhook payload guide](https://github.com/mimalef70/goomni/blob/main/docs/webhook-payload.md) for HMAC verification, event examples and receipt semantics.''',
 'Service': '''Administrative Basic authentication grants access to every configured account; tenant authorization belongs in the consuming application. Only /health in the Basic API is unauthenticated. The optional embedded UI has a public static shell and separate browser session login. /ready measures service readiness, not successful recovery for every account. /app/providers reports runtime availability and ordinary send kinds, explicit text-limit units, mentions/replies and media format notes. /app/capabilities requires an explicit provider and lists its extension contracts only; core login, device, text/media, history and webhook routes are documented separately. Named /operations calls are a whitelist with typed input, never an arbitrary provider RPC proxy. Check per-operation verification and the README release status before relying on a feature.''',
}
operation_notes={
 'group.permissions.set': 'A patch requires at least one explicit boolean. Empty, null and unknown permissions are rejected; replace requires all 20 fields, including false values. Concurrent external client changes cannot be protected by a provider compare-and-swap.',
 'group.default_permissions.set': 'A patch requires at least one explicit boolean. Empty, null and unknown permissions are rejected; replace requires all 20 fields, including false values. History sharing is configured independently.',
 'group.join': 'Accepts a raw invitation token or an exact http(s)://ble.ir/join/TOKEN link, not arbitrary URLs.',
 'group.preview': 'Accepts a raw invitation token or an exact http(s)://ble.ir/join/TOKEN link, not arbitrary URLs.',
 'account.settings.set': 'Keys contain 1–128 ASCII letters/digits or _.:-. Values have at most 8192 characters; null removes the setting and an empty string remains a value. Credential-like names are rejected.',
 'account.session.terminate': 'The session must first appear in this selected account current session list. No guessed cross-account session IDs are accepted.',
 'contacts.import': 'Imports 1–100 distinct normalized phones. requested_count is not a promise that every number appears in returned contacts.',
 'send.poll': 'This is a two-step provider operation: create the poll, then send its message. Creation has no request-ID field. Once creation may have succeeded, a later failure is unknown even if sending was not written. A crash may leave an orphan poll; there is no safe automatic recreation. Success includes poll_id, message_id and acknowledgment date.',
 'send.sticker': 'Resolve collection and sticker IDs from this account inventory. Use its animated value; do not infer it merely from a file extension.',
 'send.contact': 'name has 1–128 characters; phones has 1–10 distinct normalized numbers; at most 10 distinct emails, each up to 254 bytes. No external contact photo is fetched. A contact card does not establish a discoverable Bale account.',
 'send.location': 'Latitude is finite and within [-90,90]; longitude is finite and within [-180,180]. Zero is a valid coordinate. Optional reply_message_id retains its signed decimal representation.',
 'send.template': 'Normal-user live sends currently fail with PROVIDER_INVALID_ARGUMENT; do not rely on this operation for production keyboards until provider compatibility is established. Text has 1–16384 Unicode characters. Supply exactly one inline_keyboard, reply_keyboard or remove_keyboard. Keyboards have 1–20 rows, 1–8 buttons per row, at most 100 buttons and labels of 1–64 characters. Each inline button has exactly one URL, 1–64-byte callback_data, 1–256-character copy_text or web_app_url. A reply button is plain text or exactly one contact/location request or web_app_url. Removal must be true and is the only mode accepting selective. URLs have at most 2048 bytes without userinfo; web apps require HTTPS, ordinary links HTTP(S). They are never fetched. Login/auth/user/chat/poll request buttons and nested media templates are unsupported.',
 'story.list': 'get_unmutual is optional. Arbitrary user filters are not supported; a bounded list is not an assertion that no other stories exist.',
 'story.add': 'Supply exactly one nonempty text (1–4096 Unicode characters) or a selected-account PNG/JPEG/GIF media_id. expiration_days is 1 (default) or 2. Privacy defaults to unknown, retaining provider behavior; exclude/include use pre-existing provider lists that this endpoint does not configure. tag_ids contains at most 32 distinct positive int32 values.',
 'story.react': 'Actions are view, emoji, remove_emoji and link. Emoji needs reaction text; view and removal omit it. User is the default story type; unavailable channel/bot variants fail explicitly.',
 'message.upvoters': 'next is opaque unpadded base64url with at most 4096 decoded bytes. Do not reinterpret the cursor as UTF-8 or infer unsupported group/channel subtype from quota counts.',
}
for path,operations in paths.items():
 for method,operation in operations.items():
  name=operation.get('x-goomni-operation')
  if name in operation_notes:
   operation['description']=operation.get('description','')+'\n\n'+operation_notes[name]
voice=paths['/send/voice']['post']
voice['description']+='''\n\nAccepted Content-Type values are audio/ogg, application/ogg and audio/opus, optionally with codecs=opus. Bytes must be a complete single logical Ogg Opus v1 stream, mapping family 0, mono or stereo. Renaming MP3, WAV or WebM is not conversion. CRC, page/packet continuity, Opus framing and end-of-stream are checked; packets are bounded to 64 KiB. Duration is floor((final granule - pre-skip)/48) milliseconds and must be positive and fit int32. File size also obeys the deployment media limit. Duration/waveform are not caller inputs; outgoing MIME is audio/ogg. Validation happens in the durable send worker before provider upload, so a successful /media upload can still produce a failed operation: INVALID_MEDIA, VOICE_FORMAT_NOT_SUPPORTED or MEDIA_TOO_LARGE. Optional caption/reply and scheduled kind=voice use the same rules. Bale native receipt, display and byte-identical download were verified; browser audio playback was not part of that claim. Eitaa and Rubika have offline format tests only, with live playback and interoperability pending.'''
voice['requestBody']['content']['application/json']['example']={'peer':{'type':'user','id':'123456'},'media_id':'your-uploaded-media-id','message':'A quick voice update'}
paths['/user/avatar']['get']['description']+='''\n\nThe default size is small; large selects that rendition when present. An absent/hidden avatar yields AVATAR_NOT_FOUND without distinguishing privacy from absence. Only user peers are accepted, including self; group/channel avatars are not implied. The original image bytes are returned without re-encoding. Errors include INVALID_PEER/INVALID_REQUEST, PEER_NOT_FOUND, AVATAR_TOO_LARGE, AVATAR_INVALID, AVATAR_DOWNLOAD_FAILED, UNTRUSTED_MEDIA_URL and RESOURCE_BUSY. Responses are no-store and nosniff.'''
paths['/send/schedules']['post']['requestBody']['content']['application/json']['example']={'operation':'send.poll','payload':{'peer':{'type':'group','id':'77'},'question':'Ready?','options':['Yes','Later']},'scheduled_at':'2030-01-01T08:00:00+03:30','timezone':'Asia/Tehran'}
for path,operations in paths.items():
 for operation in operations.values():
  if operation.get('x-goomni-operation')=='group.default_permissions.set':
   operation['requestBody']['content']['application/json']['example']={'peer':{'type':'group','id':'77'},'mode':'patch','permissions':{'invite_user':True}}


# Mirror only the finite administrative router into its browser namespace. Copy
# the completed Basic operation so request/response contracts cannot drift, but
# never inherit Basic authentication or expose sends/native operations to cookies.
admin_function=re.search(r'func \(s \*Server\) registerAdminRoutes\(r fiber.Router\) \{(.*?)\n\}',source,re.S)
if not admin_function:raise RuntimeError('Administrative route registry not found')
admin_routes=set((method.upper(),re.sub(r':([a-z_]+)',r'{\1}',path)) for method,path in re.findall(r'r\.(Get|Post|Put|Patch|Delete)\("([^"\n]+)"',admin_function.group(1)))
if not admin_routes:raise RuntimeError('Administrative route registry is empty')
tag_notes['Browser administration']='The embedded panel uses this finite administrative API with the goomni_admin HttpOnly cookie. Basic credentials are not accepted here, and this cookie cannot authenticate ordinary Basic API routes. Every mutation requires an exact permitted Origin and X-CSRF-Token from the browser session response. Account-scoped reads and writes also require X-Device-Instance matching the selected immutable connection. Missing instance is 400 DEVICE_INSTANCE_REQUIRED; replacement is 409 DEVICE_INSTANCE_CHANGED before any provider call. UI_UNAUTHORIZED means the browser session is missing or expired. An account-level AUTH_REQUIRED may also use HTTP 401 without invalidating the administrative session; reauthenticate that messenger account only. No sends, generic operations, media or conversation endpoints are exposed in this namespace. All routes use APP_BASE_PATH when configured.'
for method,path in sorted(admin_routes):
 source_operation=paths.get(path,{}).get(method.lower())
 if source_operation is None:raise RuntimeError('Missing Basic contract for administrative route: '+method+' '+path)
 operation=copy.deepcopy(source_operation)
 operation['operationId']='ui_'+source_operation['operationId']
 operation['tags']=['Browser administration']
 operation['security']=[{'browserSession':[]}]
 operation['description']=('Browser-session version of '+method+' '+path+'. Basic authentication is not accepted. '+operation.get('description','')).strip()
 mutation=method not in ['GET','HEAD','OPTIONS']
 operation['parameters'].append({'name':'Origin','in':'header','required':mutation,'schema':{'type':'string'},'description':'Exact permitted browser origin. Required for mutations; cross-origin values are rejected for every request.'})
 if mutation:operation['parameters'].append({'name':'X-CSRF-Token','in':'header','required':True,'schema':{'type':'string'},'description':'Use csrf_token from the current /ui/auth/session response. Never put it in a URL.'})
 scoped=False
 for parameter in operation['parameters']:
  if parameter['name']=='X-Device-Instance' and parameter['in']=='header':
   parameter['required']=True
   parameter['description']='Required immutable instance_id from the selected device. Missing is 400 DEVICE_INSTANCE_REQUIRED; a deleted/recreated alias is 409 DEVICE_INSTANCE_CHANGED before any provider call.'
   scoped=True
 if ('{device_id}' in path or path.startswith('/deliveries')) and not scoped:raise RuntimeError('Administrative account route lacks instance guard contract: '+method+' '+path)
 operation['responses']['401']['description']='UI_UNAUTHORIZED: administrative browser cookie is absent or expired. AUTH_REQUIRED: only the selected messenger account needs authentication; this provider error does not invalidate the browser session.'
 operation['responses']['403']=copy.deepcopy(operation['responses']['400'])
 operation['responses']['403']['description']='UI_ORIGIN_REJECTED or UI_CSRF_REJECTED: exact permitted origin and, for mutations, current CSRF token are required. A provider may also reject an account operation.'
 if scoped:
  operation['responses']['400']['description']+='; DEVICE_INSTANCE_REQUIRED when the browser omits the selected instance.'
  operation['responses']['409']['description']+='; DEVICE_INSTANCE_CHANGED when the selected alias now refers to a different immutable connection.'
 if method=='POST' and path.endswith('/login'):
  operation['responses']['429']['description']='AUTH_RESEND_TOO_SOON: wait for Retry-After before explicitly requesting another code. Provider authentication limits may also return 429.'
  operation['responses']['429'].setdefault('headers',{})['Retry-After']={'schema':{'type':'integer','minimum':1},'description':'Remaining local resend cooldown in seconds, when supplied.'}
 paths.setdefault('/ui/api'+path,{})[method.lower()]=operation
for method,operation in paths['/ui/auth/session'].items():
 operation['responses']['401']['description']='UI_UNAUTHORIZED: invalid administrative credentials on POST, or missing/expired browser session on GET and DELETE. This is distinct from a messenger account AUTH_REQUIRED response.'
operation_ids=[operation['operationId'] for operations in paths.values() for operation in operations.values()]
if len(operation_ids)!=len(set(operation_ids)):raise RuntimeError('Duplicate OpenAPI operationId')

spec={'openapi':'3.0.3','info':{'title':'GoOmni REST API','version':app_version,'description':'Native Go, multi-account messenger gateway. Bale is enabled by default. Native Eitaa and Rubika adapters are opt-in and have offline protocol fixtures; live acceptance is pending. See the README release status and per-operation verification for tested provider coverage; the release version does not imply every native operation has been verified. Administrative auth grants access to every device; tenant ownership belongs in the consuming application.','contact':{'name':'GoOmni maintainers','url':'https://github.com/mimalef70/goomni/issues'},'license':{'name':'MIT','url':'https://github.com/mimalef70/goomni/blob/main/LICENCE.txt'}},'externalDocs':{'description':'Guides and verified capabilities','url':'https://github.com/mimalef70/goomni/blob/main/README.md#connect-your-application'},'tags':[{'name':name,**({'description':tag_notes[name]} if name in tag_notes else {})} for name in sorted({tag for operations in paths.values() for operation in operations.values() for tag in operation['tags']})],'servers':[{'url':'http://127.0.0.1:3000','description':'Local development; configure APP_BASE_PATH when used.'}],'paths':paths,'components':{'securitySchemes':{'basicAuth':{'type':'http','scheme':'basic'},'browserSession':{'type':'apiKey','in':'cookie','name':'goomni_admin','description':'HttpOnly browser session restricted to the UI path. Browser mutations also require CSRF and exact origin checks; this cookie does not authenticate the Basic API.'}},'schemas':schemas}}
def check_refs(value):
 if isinstance(value,dict):
  if '$ref' in value:
   target=value['$ref']
   if not target.startswith('#/components/schemas/') or target.rsplit('/',1)[1] not in schemas:
    raise RuntimeError('Unresolved schema reference: '+target)
  for child in value.values():check_refs(child)
 elif isinstance(value,list):
  for child in value:check_refs(child)
def normalize_schema_arrays(value):
 if isinstance(value,dict):
  if value.get('required')==[]:value.pop('required')
  for child in value.values():normalize_schema_arrays(child)
 elif isinstance(value,list):
  for child in value:normalize_schema_arrays(child)
normalize_schema_arrays(spec)
check_refs(spec)
class SpecDumper(yaml.SafeDumper):
 def ignore_aliases(self, data):
  return True

parser=argparse.ArgumentParser(description=__doc__)
parser.add_argument('--check',action='store_true',help='Verify the canonical YAML is current without rewriting it.')
parser.add_argument('--format',choices=['yaml','json'],default='yaml',help='JSON is available only with --stdout for compatibility consumers.')
parser.add_argument('--stdout',action='store_true',help='Write the generated contract to stdout instead of a file.')
args=parser.parse_args()
if args.check and args.stdout:parser.error('--check and --stdout are mutually exclusive')
if args.format=='json' and not args.stdout:parser.error('--format json requires --stdout; only YAML is tracked')
data=(json.dumps(spec,ensure_ascii=False,indent=2)+'\n' if args.format=='json' else yaml.dump(spec,Dumper=SpecDumper,sort_keys=False,allow_unicode=True,width=110))
target=root/'docs/openapi.yaml'
contract_target=root/'src/config/contract.go'
contract='// Code generated by scripts/generate_openapi.py; DO NOT EDIT.\npackage config\n\nconst ContractSHA256 = "'+hashlib.sha256(data.encode()).hexdigest()+'"\n'
if args.stdout:sys.stdout.write(data)
elif args.check:
 if not contract_target.exists() or contract_target.read_text()!=contract:raise SystemExit('OpenAPI digest stale: run python3 scripts/generate_openapi.py')
 if not target.exists() or target.read_text()!=data:raise SystemExit('OpenAPI stale: run python3 scripts/generate_openapi.py')
else:
 target.write_text(data)
 contract_target.write_text(contract)
if not args.stdout:print(f'{len(paths)} paths / {sum(len(operations) for operations in paths.values())} operations')
