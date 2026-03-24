➜  ~ sh test.sh 
event: message_start
data: {"type":"message_start","message":{"model":"claude-opus-4-6","id":"msg_01Tgf4f9noPrjBcbKX1r94gi","type":"message","role":"assistant","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":37,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0},"output_tokens":4,"service_tier":"standard","inference_geo":"global"}}        }

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}               }

event: ping
data: {"type": "ping"}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"Ev8BCkYICxgCKkBfsesDm3r2VMqS5DXukQvZuhJucFRonUZ6qz32vLV3+cn3sdZ2KxkGNgVHeLkW871jXKmUD0teI1ImUk7HI1FTEgx0fhpRFdgDYP6hD6oaDKMZzffj9/FeqplzFCIw95Eu+dtBOlhklJhC6a8V1EGLQB5unKRalRmNTuVw8tt+0iKnbuQErHP+kV7xe2aCKmeOaGdurxiMoKhwUCWYLvemWGtad2MVIUuAxpQT3Yzk/8akh1HShezrSqnin5no9so9nsIZjoTN20MxBkerP9Lk1vSKwmy23MUrRy+fjGAqHUnDs6/AngQCgiO6705f0dGH8qaiBF+3GAE="}          }

event: content_block_stop
data: {"type":"content_block_stop","index":0         }

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""} }

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Hi"}       }

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":" there! "}           }

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"👋 "}           }

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"How"}           }

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":" are"}             }

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":" you doing?"}     }

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":" Is there anything I can help you with"}     }

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":" today?"}           }

event: content_block_stop
data: {"type":"content_block_stop","index":1 }

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"input_tokens":37,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":56},"context_management":{"applied_edits":[]}               }

event: message_stop
data: {"type":"message_stop"     }


HTTP_STATUS: 200
➜  ~ 