import React, { useEffect, useState } from 'react';
import { Button, Card, Form, Input, InputNumber, Modal, Popconfirm, Select, Space, Table, Tag, message } from 'antd';
import { apiGet, apiPost, safeJsonParse, unixTime } from '../lib/api';
import JsonModal from '../components/JsonModal';

const serverTypes = ['vmess','vless','trojan','shadowsocks','hysteria','tuic','anytls','v2node'];
const protocols = ['vmess','vless','trojan','shadowsocks','hysteria2','tuic','anytls'];
const networks = ['tcp','ws','grpc','h2','http'];

function normalizePayload(values: any, edit: any) {
  const payload: any = { ...edit, ...values };
  if (values.payload) {
    const extra = safeJsonParse(values.payload, {});
    if (extra && typeof extra === 'object' && !Array.isArray(extra)) Object.assign(payload, extra);
  }
  delete payload.payload;
  delete payload.server_type;
  payload.group_id = safeJsonParse(payload.group_id, payload.group_id);
  payload.route_id = safeJsonParse(payload.route_id, payload.route_id);
  payload.tags = safeJsonParse(payload.tags, payload.tags);
  ['network_settings','networkSettings','tls_settings','tlsSettings','encryption_settings','ruleSettings','dnsSettings','obfs_settings'].forEach((key)=>{
    if (payload[key] !== undefined) payload[key] = safeJsonParse(payload[key], payload[key]);
  });
  if (payload.show === true) payload.show = 1;
  if (payload.show === false) payload.show = 0;
  return payload;
}

export default function ServerManage(){
 const [rows,setRows]=useState<any[]>([]),[loading,setLoading]=useState(false),[edit,setEdit]=useState<any>(null),[detail,setDetail]=useState<any>(null); const [form]=Form.useForm();
 const load=async()=>{setLoading(true);try{const r=await apiGet('/server/manage/getNodes');setRows(r.data||[])}catch(e:any){message.error(e.message||'加载失败')}finally{setLoading(false)}}; useEffect(()=>{load()},[]);
 const save=async()=>{const v=await form.validateFields(); const type=v.type||edit?.type||edit?.server_type; const id=edit?.id; const path=id?`/server/${type}/save`:`/server/${type}/save`; await apiPost(path,normalizePayload({...v,id},edit)); message.success('保存成功'); setEdit(null); load();};
 const del=async(r:any)=>{const type=r.type||r.server_type; await apiPost(`/server/${type}/drop`,{id:r.id},{form:true}); message.success('已删除'); load();};
 const openEditor=(row:any)=>{setEdit(row);form.setFieldsValue({...row,type:row.type||row.server_type,group_id:Array.isArray(row.group_id)?JSON.stringify(row.group_id):row.group_id,route_id:Array.isArray(row.route_id)?JSON.stringify(row.route_id):row.route_id,tags:Array.isArray(row.tags)?JSON.stringify(row.tags):row.tags,payload:JSON.stringify(row,null,2)})};
 return <div className="page-stack"><Card title="节点管理" extra={<Space><Button onClick={load}>刷新</Button><Button type="primary" onClick={()=>{setEdit({type:'v2node',show:1});form.resetFields();form.setFieldsValue({type:'v2node',protocol:'vless',network:'tcp',tls:0,show:1,rate:1,server_port:443,group_id:'[1]'})}}>新增节点</Button></Space>}><Table rowKey={(r)=>`${r.type||r.server_type}-${r.id}`} loading={loading} dataSource={rows} scroll={{x:'max-content'}} columns={[
  {title:'ID',dataIndex:'id',width:70},{title:'类型',render:(_:any,r:any)=><Tag>{r.type||r.server_type}</Tag>},{title:'名称',dataIndex:'name'},{title:'地址',dataIndex:'host'},{title:'入口IP',dataIndex:'listen_ip'},{title:'出口IP',dataIndex:'send_through'},{title:'端口',dataIndex:'port'},{title:'倍率',dataIndex:'rate'},{title:'入口组',dataIndex:'entry_group_name'},{title:'更新',dataIndex:'updated_at',render:unixTime},
  {title:'操作',fixed:'right',width:250,render:(_:any,r:any)=><Space wrap><Button size="small" onClick={()=>setDetail(r)}>详情</Button><Button size="small" onClick={()=>openEditor(r)}>编辑</Button><Button size="small" onClick={async()=>{await apiPost(`/server/${r.type||r.server_type}/copy`,{id:r.id},{form:true});message.success('已复制');load()}}>复制</Button><Popconfirm title="确认删除？" onConfirm={()=>del(r)}><Button size="small" danger>删除</Button></Popconfirm></Space>}
 ] as any}/></Card>
 <Modal width={860} title="节点" open={!!edit} onCancel={()=>setEdit(null)} onOk={save}><Form form={form} layout="vertical"><Form.Item name="type" label="类型" rules={[{required:true}]}><Select options={serverTypes.map(x=>({label:x,value:x}))}/></Form.Item><Form.Item name="protocol" label="v2node 协议"><Select allowClear options={protocols.map(x=>({label:x,value:x}))}/></Form.Item><Form.Item name="name" label="名称" rules={[{required:true}]}><Input/></Form.Item><Form.Item name="host" label="连接地址" rules={[{required:true}]}><Input/></Form.Item><Form.Item name="listen_ip" label="监听IP"><Input/></Form.Item><Form.Item name="send_through" label="出口IP"><Input/></Form.Item><Form.Item name="port" label="连接端口" rules={[{required:true}]}><Input/></Form.Item><Form.Item name="server_port" label="后端端口" rules={[{required:true}]}><InputNumber style={{width:'100%'}}/></Form.Item><Form.Item name="group_id" label="权限组 JSON/逗号" rules={[{required:true}]}><Input/></Form.Item><Form.Item name="route_id" label="路由组 JSON/逗号"><Input/></Form.Item><Form.Item name="rate" label="倍率" rules={[{required:true}]}><InputNumber style={{width:'100%'}}/></Form.Item><Form.Item name="network" label="传输"><Select allowClear options={networks.map(x=>({label:x,value:x}))}/></Form.Item><Form.Item name="tls" label="TLS"><Select options={[{label:'关闭',value:0},{label:'开启',value:1}]}/></Form.Item><Form.Item name="show" label="显示"><Select options={[{label:'隐藏',value:0},{label:'显示',value:1}]}/></Form.Item><Form.Item name="tags" label="标签 JSON/逗号"><Input/></Form.Item><Form.Item name="payload" label="高级 JSON"><Input.TextArea rows={8} placeholder="复杂协议字段可填 JSON，会覆盖上方同名字段"/></Form.Item></Form></Modal>
 <JsonModal open={!!detail} title="节点详情" data={detail} onClose={()=>setDetail(null)}/></div>
}
