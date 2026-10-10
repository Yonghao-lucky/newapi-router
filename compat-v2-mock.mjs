import http from 'node:http'
let calls = 0
let last = null
http.createServer(async (req, res) => {
  if (req.url === '/stats') { res.setHeader('Content-Type','application/json'); res.end(JSON.stringify({calls,last})); return }
  let body = ''
  for await (const chunk of req) { body += chunk; if (body.length > 1048576) { res.writeHead(413);res.end();return } }
  const input = JSON.parse(body)
  calls++
  last = input
  const disabled = input.thinking?.type === 'disabled'
  const message = {role:'assistant',content:'42',...(disabled ? {} : {reasoning_content:'fixture reasoning'})}
  const usage = {prompt_tokens:10,completion_tokens:disabled ? 2 : 5,total_tokens:disabled ? 12 : 15,completion_tokens_details:{reasoning_tokens:disabled ? 0 : 3}}
  res.setHeader('Content-Type',input.stream?'text/event-stream':'application/json')
  if(input.stream){res.end(`data: ${JSON.stringify({id:'fixture',object:'chat.completion.chunk',model:'kimi-k3',choices:[{index:0,delta:message,finish_reason:null}]})}\n\ndata: ${JSON.stringify({id:'fixture',model:'kimi-k3',choices:[{index:0,delta:{},finish_reason:'stop'}],usage})}\n\ndata: [DONE]\n\n`)}
  else res.end(JSON.stringify({id:'fixture',object:'chat.completion',model:'kimi-k3',choices:[{index:0,message,finish_reason:'stop'}],usage}))
}).listen(8080,'0.0.0.0')
